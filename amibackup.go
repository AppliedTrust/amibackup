package main

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/docopt/docopt-go"
)

const version = "0.14-20171229"

var usage = `amibackup: create cross-region AWS AMI backups

Usage:
  amibackup [options] [-p <window>]...  [-i <volume>]...  <instance_name_tag>...
  amibackup -h --help
  amibackup --version

Options:
  -s, --source=<region>     AWS region of running instance [default: us-east-1].
  -d, --dest=<region>       AWS region to store backup AMI [default: us-west-1].
  -t, --timeout=<secs>      Timeout waiting for AMI creation [default: 30m].
  -e, --encrypted           Encrypts the EBS volumes attached to the ami with key supplied by -k, or the accounts default KMS key. [default: false]
  -k, --kms-key-id=<keyid>  KMS key arn for encrypted EBS volumes. Implies -e.
  -p, --purge=<window>      One or more purge windows - see below for details.
  -o, --purgeonly           Purge old AMIs without creating new ones.
  -D, --dry-run             Do not actually create or purge anything, just say what would have happened.
  -i, --ignore=<volume>     Ignore volume mounted at this mount point - multiple use ok.
  --version                 Show version.
  -h, --help                Show this screen.

AWS Authentication:
  Either setup a ~/.aws/credentials file (~/.aws/config NOT supported)
	OR set the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.

Purge windows:
  Delete old AMIs (and associated snapshots) based on the Purge windows you define.
  By default AMIs are kept.  AMIs in specified Purge Windows are purged.
  Purge Window format is: PURGE_INTERVAL:PURGE_START:PURGE_END
  Each is a time interval (second/minute/hour/day), such as: 1s:4m:9d
  Where:
    PURGE_INTERVAL  time interval in which to keep one backup
    PURGE_START     start purging (ago)
    PURGE_END       end purging (ago)
  Sample purge schedule:
  -p 1d:4d:30d -p 7d:30d:90d -p 30d:90d:180d   Keep all for past 4 days, 1/day for past 30 days, 1/week for past 90 days, 1/mo forever.
`

var apiPollInterval = 15 * time.Second

type window struct {
	interval time.Duration
	start    time.Time
	stop     time.Time
}
type Config struct {
	dryRun             bool
	errorLevel         int
	instanceNameTags   []string
	sourceRegion       string
	destRegion         string
	timeoutString      string
	kmsKeyId           string
	timeout            time.Duration
	windows            []window
	purgeonly          bool
	encrypted          bool
	ignoreVolumes      []string
	awsAccessKeyId     string
	awsSecretAccessKey string
}

// time formatting
var timeSecs = fmt.Sprintf("%d", time.Now().Unix())
var timeStamp = time.Now().Format("2006-01-02_15-04-05")
var timeShortFormat = "01/02/2006@15:04:05"
var timeString = time.Now().Format("2006-01-02 15:04:05 -0700")

func main() {
	c := handleOptions()
	go func() {
		time.Sleep(c.timeout)
		log.Fatalf("Hit timeout of %s before we finished - goodbye!", c.timeoutString)
	}()

	// connect to AWS
	awsec2 := ec2.New(session.New(), &aws.Config{Region: aws.String(c.sourceRegion)})
	awsec2dest := ec2.New(session.New(), &aws.Config{Region: aws.String(c.destRegion)})

	// purge old AMIs and snapshots in both regions
	if len(c.windows) > 0 {
		for _, instanceNameTag := range c.instanceNameTags {
			err := purgeAMIs(awsec2, c.sourceRegion, instanceNameTag, c)
			if err != nil {
				log.Printf("Error purging old AMIs for %s in %s: %s", instanceNameTag, c.sourceRegion, err.Error())
			}
			if c.destRegion != c.sourceRegion {
				err = purgeAMIs(awsec2dest, c.destRegion, instanceNameTag, c)
				if err != nil {
					log.Printf("Error purging old AMIs for %s in %s: %s", instanceNameTag, c.destRegion, err.Error())
				}
			}
		}
	}
	if c.purgeonly {
		log.Printf("Purging done and --purgeonly specified - exiting.")
		return
	}

	// search for our instances
	instanceset := map[string][]*ec2.Instance{}
	for _, instanceNameTag := range c.instanceNameTags {
		instanceset[instanceNameTag] = findInstances(awsec2, instanceNameTag)
		if len(instanceset[instanceNameTag]) < 1 {
			log.Fatalf("No instances with matching name tag: %s", instanceNameTag)
		} else {
			log.Printf("Found %d instances with matching Name tag: %s", len(instanceset[instanceNameTag]), instanceNameTag)
		}
	}

	done := make(chan string)
	i := 0
	for instanceNameTag, instances := range instanceset {
		for _, instance := range instances {
			i++
			instanceNameTag := instanceNameTag
			instance := instance
			go func() {
				defer func() {
					done <- instanceNameTag
				}()

				// create local AMI
				newAMI, err := createAMI(awsec2, instance, c, instanceNameTag)
				if err != nil {
					log.Printf("Error creating AMI for %s: %s", instanceNameTag, err.Error())
					return
				}

				// copy AMI to backup region
				if err := copyAMI(awsec2dest, c, newAMI, instance, instanceNameTag); err != nil {
					log.Printf("Error copying AMI for %s: %s", instanceNameTag, err.Error())
					return
				}
				// find and tag snaphots
				err = findTagVolumeSnapshots(instanceNameTag, awsec2, awsec2dest)
				if err != nil {
					log.Printf("Error Tagging Snapshots for %s: %s", instanceNameTag, err.Error())
					return
				}
			}()
		}
	}

	for _, instances := range instanceset {
		for _, _ = range instances {
			n := <-done // wait for everyone to finish
			log.Printf("All done with %s", n)
		}
	}
	log.Printf("All done!")
}

// findInstances searches for our instances by "Name" tag
func findInstances(awsec2 *ec2.EC2, instanceNameTag string) []*ec2.Instance {
	params := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{{
			Name:   aws.String("tag:Name"),
			Values: []*string{aws.String(instanceNameTag)},
		}},
	}
	resp, err := awsec2.DescribeInstances(params)
	if err != nil {
		log.Fatalf("EC2 API DescribeInstances failed: %s", err.Error())
	}
	instances := []*ec2.Instance{}
	for _, reservation := range resp.Reservations {
		for _, instance := range reservation.Instances {
			instances = append(instances, instance)
		}
	}
	return instances
}

// findSnapshots returns a map of snapshots associated with an AMI
func findSnapshots(amiid string, awsec2 *ec2.EC2) (map[string]string, error) {
	snaps := make(map[string]string)
	resp, err := awsec2.DescribeImages(&ec2.DescribeImagesInput{ImageIds: []*string{aws.String(amiid)}})
	if err != nil {
		return snaps, fmt.Errorf("EC2 API DescribeImages failed: %s", err.Error())
	}
	for _, image := range resp.Images {
		for _, bd := range image.BlockDeviceMappings {
			if bd.Ebs != nil && len(*bd.Ebs.SnapshotId) > 0 {
				snaps[*bd.Ebs.SnapshotId] = *bd.DeviceName
			}
		}
	}
	return snaps, nil
}

func findAMIs(instanceNameTag string, awsec2 *ec2.EC2, awsdestec2 *ec2.EC2) (map[string][]*ec2.Tag, error) {
	amis := make(map[string][]*ec2.Tag)
	resp, err := awsec2.DescribeImages(&ec2.DescribeImagesInput{Filters: []*ec2.Filter{{
		Name:   aws.String("tag:hostname"),
		Values: []*string{aws.String(instanceNameTag)},
	}}})
	if err != nil {
		return nil, err
	}
	for _, image := range resp.Images {
		for _, tag := range image.Tags {
			amis[*image.ImageId] = append(amis[*image.ImageId], tag)
		}
	}
	resp, err = awsdestec2.DescribeImages(&ec2.DescribeImagesInput{Filters: []*ec2.Filter{{
		Name:   aws.String("tag:hostname"),
		Values: []*string{aws.String(instanceNameTag)},
	}}})
	if err != nil {
		return nil, err
	}
	for _, image := range resp.Images {
		for _, tag := range image.Tags {
			amis[*image.ImageId] = append(amis[*image.ImageId], tag)
		}
	}
	return amis, nil
}

func TagVolumeSnapshots(instanceNameTag string, awsec2 *ec2.EC2, amis map[string][]*ec2.Tag) error {
	resp, err := awsec2.DescribeSnapshots(&ec2.DescribeSnapshotsInput{})
	if err != nil {
		fmt.Println(err)
		return err
	}
	for _, snapshot := range resp.Snapshots {
		re, err := regexp.Compile(`ami-\w*`)
		if err == nil {
			res := re.FindStringSubmatch(*snapshot.Description)
			if len(res) > 0 {
				snapshot_ami := res[0]
				if amis[snapshot_ami] != nil {
					fmt.Println("Tagging " + *snapshot.SnapshotId)
					_, err := awsec2.CreateTags(&ec2.CreateTagsInput{
						Resources: []*string{aws.String(*snapshot.SnapshotId)},
						Tags:      amis[snapshot_ami],
					})
					if err != nil {
						fmt.Println(err)
						return err
					}
				}
			}
		}
	}
	return nil
}

// Finds and tags volume snapshots
func findTagVolumeSnapshots(instanceNameTag string, awsec2 *ec2.EC2, awsdestec2 *ec2.EC2) error {
	amis, err := findAMIs(instanceNameTag, awsec2, awsdestec2)
	if err != nil {
		return err
	}
	fmt.Println(amis)
	err = TagVolumeSnapshots(instanceNameTag, awsec2, amis)
	err = TagVolumeSnapshots(instanceNameTag, awsdestec2, amis)
	return nil
}

// createAMI actually creates the AMI
func createAMI(awsec2 *ec2.EC2, instance *ec2.Instance, c *Config, instanceNameTag string) (string, error) {
	newAMI := ""

	backupAmiName := fmt.Sprintf("%s-%s-%s", instanceNameTag, timeStamp, *instance.InstanceId)
	backupDesc := fmt.Sprintf("%s %s %s", instanceNameTag, timeString, *instance.InstanceId)
	blockDevices := []*ec2.BlockDeviceMapping{}
	for _, i := range c.ignoreVolumes {
		blockDevices = append(blockDevices, &ec2.BlockDeviceMapping{DeviceName: aws.String(i), NoDevice: aws.String("")})
	}
	params := &ec2.CreateImageInput{
		InstanceId:  instance.InstanceId,
		Name:        aws.String(backupAmiName),
		Description: aws.String(backupDesc),
		NoReboot:    aws.Bool(true),
	}
	if len(blockDevices) > 0 {
		params.BlockDeviceMappings = blockDevices
	}
	if !c.dryRun {
		resp, err := awsec2.CreateImage(params)
		if err != nil {
			return newAMI, fmt.Errorf("Error creating new AMI named %s for instance %s: %s", backupAmiName, *instance.InstanceId, err.Error())
		}
		newAMI = *resp.ImageId
		log.Printf("Creating new AMI %s for %s (%s)", *resp.ImageId, instanceNameTag, *instance.InstanceId)
	} else {
		log.Printf("DRYRUN: would have created AMI for: %s (%s)", instanceNameTag, *instance.InstanceId)
	}
	if err := waitForAMI(awsec2, newAMI, instanceNameTag, false); err != nil {
		return newAMI, err
	}
	log.Printf("Created new AMI %s in region %s", newAMI, c.sourceRegion)

	// tag the AMI
	_, err := awsec2.CreateTags(&ec2.CreateTagsInput{
		Resources: []*string{aws.String(newAMI)},
		Tags: []*ec2.Tag{
			{Key: aws.String("hostname"), Value: aws.String(instanceNameTag)},
			{Key: aws.String("instance"), Value: instance.InstanceId},
			{Key: aws.String("date"), Value: aws.String(timeString)},
			{Key: aws.String("timestamp"), Value: aws.String(timeSecs)},
		},
	})
	return newAMI, err
}

// wait for AMI to be ready
func waitForAMI(awsec2 *ec2.EC2, newAMI, instanceNameTag string, isCopy bool) error {
	jobstate := "new"
	for {
		if isCopy {
			log.Printf("Waiting for %s AMI copy %s for %s", jobstate, newAMI, instanceNameTag)
		} else {
			log.Printf("Waiting for %s AMI %s for %s", jobstate, newAMI, instanceNameTag)
		}
		time.Sleep(apiPollInterval)
		resp, err := awsec2.DescribeImages(&ec2.DescribeImagesInput{ImageIds: []*string{aws.String(newAMI)}})
		if err != nil {
			log.Printf("Error waiting for new AMI %s for instance %s (trying again): %s", newAMI, instanceNameTag, err.Error())
			continue
		}
		for _, image := range resp.Images {
			jobstate = *image.State
			if jobstate == "available" {
				return nil
			}
		}
	}
}

// copyAMI starts the AMI copy
func copyAMI(awsec2dest *ec2.EC2, c *Config, amiId string, instance *ec2.Instance, instanceNameTag string) error {
	if c.dryRun {
		log.Printf("DRYRUN: would have copied new AMI from %s to %s", c.sourceRegion, c.destRegion)
		return nil
	}
	if c.destRegion != c.sourceRegion {
		backupAmiName := fmt.Sprintf("%s-%s-%s", instanceNameTag, timeStamp, amiId)
		backupDesc := fmt.Sprintf("%s %s %s", instanceNameTag, timeString, amiId)
		params := &ec2.CopyImageInput{
			SourceRegion:  aws.String(c.sourceRegion),
			SourceImageId: aws.String(amiId),
			Name:          aws.String(backupAmiName),
			Description:   aws.String(backupDesc),
			ClientToken:   aws.String(""),
		}
		if c.encrypted {
			params.Encrypted = aws.Bool(true)
			if c.kmsKeyId != "" {
				params.KmsKeyId = aws.String(c.kmsKeyId)
			} // else: uses default kms key
		}

		copyResp, err := awsec2dest.CopyImage(params)
		if err != nil {
			return fmt.Errorf("CopyImage failed: %s", err.Error())
		}
		log.Printf("Started copy of %s from %s (%s) to %s (%s).", instanceNameTag, c.sourceRegion, amiId, c.destRegion, *copyResp.ImageId)
		time.Sleep(apiPollInterval)

		_, err = awsec2dest.CreateTags(&ec2.CreateTagsInput{
			Resources: []*string{copyResp.ImageId},
			Tags: []*ec2.Tag{
				{Key: aws.String("hostname"), Value: aws.String(instanceNameTag)},
				{Key: aws.String("instance"), Value: instance.InstanceId},
				{Key: aws.String("sourceregion"), Value: aws.String(c.sourceRegion)},
				{Key: aws.String("date"), Value: aws.String(timeString)},
				{Key: aws.String("timestamp"), Value: aws.String(timeSecs)},
			},
		})

		if err != nil {
			return fmt.Errorf("Error tagging new AMI: %s", err.Error())
		}

		if err := waitForAMI(awsec2dest, *copyResp.ImageId, instanceNameTag, true); err != nil {
			return err
		}

		log.Printf("Finished copy of %s from %s (%s) to %s (%s).", instanceNameTag, c.sourceRegion, amiId, c.destRegion, *copyResp.ImageId)
	} else {
		log.Printf("Not copying AMI %s - source and dest regions match", amiId)
	}
	return nil
}

// purgeAMIs purges AMIs based on specified windows
func purgeAMIs(awsec2 *ec2.EC2, regionName, instanceNameTag string, c *Config) error {
	resp, err := awsec2.DescribeImages(&ec2.DescribeImagesInput{Filters: []*ec2.Filter{{
		Name:   aws.String("tag:hostname"),
		Values: []*string{aws.String(instanceNameTag)},
	}}})
	if err != nil {
		return fmt.Errorf("EC2 API Images failed: %s", err.Error())
	}
	log.Printf("Found %d total images for %s in %s", len(resp.Images), instanceNameTag, regionName)
	images := map[string]time.Time{}
	for _, image := range resp.Images {
		timestampTag := ""
		for _, tag := range image.Tags {
			if *tag.Key == "timestamp" {
				timestampTag = *tag.Value
			}
		}
		if len(timestampTag) < 1 {
			log.Printf("AMI is missing timestamp tag - skipping: %s", image.ImageId)
			continue
		}
		timestamp, err := strconv.ParseInt(timestampTag, 10, 64)
		if err != nil {
			log.Printf("AMI timestamp tag is corrupt - skipping: %s", image.ImageId)
			continue
		}
		images[*image.ImageId] = time.Unix(timestamp, 0)
	}
	for _, window := range c.windows {
		log.Printf("Window: 1 per %s from %s-%s", window.interval.String(), window.start, window.stop)
		for cursor := window.start; cursor.Before(window.stop); cursor = cursor.Add(window.interval) {
			cursorEnd := cursor.Add(window.interval)
			if cursorEnd.After(window.stop) {
				cursorEnd = window.stop
			}
			imagesInThisInterval := []string{}
			imagesTimes := make(map[string]time.Time)
			oldestImage := ""
			oldestImageTime := time.Now()
			for id, when := range images {
				if when.After(cursor) && when.Before(cursorEnd) {
					imagesInThisInterval = append(imagesInThisInterval, id)
					imagesTimes[id] = when
					if when.Before(oldestImageTime) {
						oldestImageTime = when
						oldestImage = id
					}
				}
			}
			if len(imagesInThisInterval) > 1 {
				for _, id := range imagesInThisInterval {
					if id == oldestImage { // keep the oldest one
						log.Printf("Keeping oldest AMI in this window: %s @ %s (%s->%s)", id, imagesTimes[id].Format(timeShortFormat), window.start.Format(timeShortFormat), window.stop.Format(timeShortFormat))
						continue
					}
					// find snapshots associated with this AMI.
					snaps, err := findSnapshots(id, awsec2)
					if err != nil {
						return fmt.Errorf("EC2 API findSnapshots failed for %s: %s", id, err.Error())
					}
					// deregister the AMI.
					if !c.dryRun {
						_, err := awsec2.DeregisterImage(&ec2.DeregisterImageInput{ImageId: aws.String(id)})
						if err != nil {
							return fmt.Errorf("EC2 API DeregisterImage failed for %s: %s", id, err.Error())
						}
					} else {
						log.Printf("DRYRUN: would have deregistered image ID: %s", id)
					}
					// delete snapshots associated with this AMI.
					for snap, _ := range snaps {
						if !c.dryRun {
							if _, err := awsec2.DeleteSnapshot(&ec2.DeleteSnapshotInput{SnapshotId: aws.String(snap)}); err != nil {
								return fmt.Errorf("EC2 API DeleteSnapshot failed for %s: %s", snap, err.Error())
							}
						} else {
							log.Printf("DRYRUN: would have deleted snapshot ID: %s", snap)
						}
					}
					if !c.dryRun {
						log.Printf("Purged old AMI %s @ %s (%s->%s)", id, imagesTimes[id].Format(timeShortFormat), window.start.Format(timeShortFormat), window.stop.Format(timeShortFormat))
					} else {
						log.Printf("DRYRUN: would have purged old AMI %s @ %s (%s->%s)", id, imagesTimes[id].Format(timeShortFormat), window.start.Format(timeShortFormat), window.stop.Format(timeShortFormat))
					}
				}
			}
		}
	}
	return nil
}

// daysToHours is a helper to support 2d notation
func daysToHours(in string) (string, error) {
	r, err := regexp.Compile(`^(\d+)d$`)
	if err != nil {
		return in, err
	}
	m := r.FindStringSubmatch(in)
	if len(m) > 0 {
		num, err := strconv.Atoi(m[1])
		if err != nil {
			return in, err
		}
		return fmt.Sprintf("%dh", num*24), nil
	}
	return in, nil
}

// handleOptions parses CLI options
func handleOptions() *Config {
	c := Config{}
	arguments, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		log.Fatalf("Error parsing arguments: %s", err.Error())
	}
	c.instanceNameTags = arguments["<instance_name_tag>"].([]string)
	c.sourceRegion = arguments["--source"].(string)
	c.destRegion = arguments["--dest"].(string)
	c.timeoutString = arguments["--timeout"].(string)
	c.timeout, err = time.ParseDuration(c.timeoutString)
	if err != nil {
		log.Fatalf("Invalid timeout: %s", arguments["--timeout"].(string))
	}
	if arguments["--purgeonly"].(bool) {
		c.purgeonly = true
	}
	if arguments["--dry-run"].(bool) {
		c.dryRun = true
	}
	if arguments["--encrypted"].(bool) || arguments["--kms-key-id"] != nil { // TODO: can i cast that into a bool?
		c.encrypted = true
		if arguments["--kms-key-id"] != nil {
			if !strings.Contains(arguments["--kms-key-id"].(string), c.destRegion) {
				log.Fatalf("kms-key-id does not reside in destination.")
			}
			c.kmsKeyId = arguments["--kms-key-id"].(string)
		}
	}
	for _, w := range arguments["--purge"].([]string) {
		newWindow := window{}
		parts := strings.Split(w, ":")
		if len(parts) != 3 {
			log.Fatalf("Malformed purge window: %s", w)
		}
		converted, err := daysToHours(parts[0])
		if err != nil {
			log.Fatalf("Malformed purge window interval: %s %s", w, err.Error())
		}
		newWindow.interval, err = time.ParseDuration(converted)
		if err != nil {
			log.Fatalf("Malformed purge window interval: %s %s", w, err.Error())
		}
		converted, err = daysToHours(parts[1])
		if err != nil {
			log.Fatalf("Malformed purge window start: %s %s", w, err.Error())
		}
		timeAgo, err := time.ParseDuration(converted)
		if err != nil {
			log.Fatalf("Malformed purge window start: %s %s", w, err.Error())
		}
		newWindow.stop = time.Now().Add(-timeAgo)
		converted, err = daysToHours(parts[2])
		if err != nil {
			log.Fatalf("Malformed purge window stop: %s %s", w, err.Error())
		}
		timeAgo, err = time.ParseDuration(converted)
		if err != nil {
			log.Fatalf("Malformed purge window stop: %s %s", w, err.Error())
		}
		newWindow.start = time.Now().Add(-timeAgo)
		c.windows = append(c.windows, newWindow)
	}

	for _, v := range arguments["--ignore"].([]string) {
		c.ignoreVolumes = append(c.ignoreVolumes, v)
	}
	return &c
}
