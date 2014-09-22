package main

import (
	"fmt"
	"github.com/crowdmob/goamz/aws"
	"github.com/docopt/docopt-go"
	"github.com/nmcclain/goamz/ec2"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const version = "0.5"

var usage = `amibackup: create cross-region AWS AMI backups

Usage:
  amibackup [options] [-p <window>]... <instance_name_tag>
  amibackup -h --help
  amibackup --version

Options:
  -s, --source=<region>     AWS region of running instance [default: us-east-1].
  -d, --dest=<region>       AWS region to store backup AMI [default: us-west-1].
  -t, --timeout=<secs>      Timeout waiting for AMI creation [default: 300].
  -p, --prune=<window>      Comma-separated list of prune windows - see below for details.
  -n, --nagios              Run like a Nagios check.
  -o, --pruneonly           Prune old AMIs without creating new ones.
  --debug                   Enable debugging output.
  --version                 Show version.
  -h, --help                Show this screen.

Purge windows:
  Clean up old AMIs (and associated snapshots) based on the Purge windows you define.
  Sample prune schedule:
  -p 1d:4d:30d -p 7d:30d:90d -p 30d:90d:180d   Keep all for past 4 days, 1/day for past 30 days, 1/week for past 90 days, 1/mo forever.
`

var apiPollInterval = 5 * time.Second

type window struct {
	interval time.Duration
	start    time.Time
	stop     time.Time
}
type session struct {
	debugMode      bool
	nagiosMode     bool
	errorLevel     int
	nagiosMessages []string
}

func main() {
	s := session{}
	// handle options
	arguments, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		s.fatal(fmt.Sprintf("Error parsing arguments: %s", err.Error()))
	}
	instanceNameTag := arguments["<instance_name_tag>"].(string)
	regionMap := map[string]aws.Region{
		"us-gov-west-1":  aws.USGovWest,
		"us-east-1":      aws.USEast,
		"us-west-1":      aws.USWest,
		"us-west-2":      aws.USWest2,
		"eu-west-1":      aws.EUWest,
		"ap-southeast-1": aws.APSoutheast,
		"ap-southeast-2": aws.APSoutheast2,
		"ap-northeast-1": aws.APNortheast,
		"sa-east-1":      aws.SAEast,
	}
	sourceRegion, ok := regionMap[arguments["--source"].(string)]
	if !ok {
		s.fatal(fmt.Sprintf("Bad region: %s", arguments["--source"].(string)))
	}
	destRegion, ok := regionMap[arguments["--dest"].(string)]
	if !ok {
		s.fatal(fmt.Sprintf("Bad region: %s", arguments["--dest"].(string)))
	}
	timeout, err := time.ParseDuration(arguments["--timeout"].(string) + "s")
	if err != nil {
		s.fatal(fmt.Sprintf("Invalid timeout: %s", arguments["--timeout"].(string)))
	}
	if arguments["--nagios"].(bool) {
		s.nagiosMode = true
	}
	if arguments["--debug"].(bool) {
		s.debugMode = true
	}
	windows := []window{}
	for _, w := range arguments["--prune"].([]string) {
		newWindow := window{}
		parts := strings.Split(w, ":")
		if len(parts) != 3 {
			s.fatal(fmt.Sprintf("Malformed prune window: %s", w))
		}
		converted, err := daysToHours(parts[0])
		if err != nil {
			s.fatal(fmt.Sprintf("Malformed prune window interval: %s %s", w, err.Error()))
		}
		newWindow.interval, err = time.ParseDuration(converted)
		if err != nil {
			s.fatal(fmt.Sprintf("Malformed prune window interval: %s %s", w, err.Error()))
		}
		converted, err = daysToHours(parts[1])
		if err != nil {
			s.fatal(fmt.Sprintf("Malformed prune window start: %s %s", w, err.Error()))
		}
		timeAgo, err := time.ParseDuration(converted)
		if err != nil {
			s.fatal(fmt.Sprintf("Malformed prune window start: %s %s", w, err.Error()))
		}
		newWindow.stop = time.Now().Add(-timeAgo)
		converted, err = daysToHours(parts[2])
		if err != nil {
			s.fatal(fmt.Sprintf("Malformed prune window stop: %s %s", w, err.Error()))
		}
		timeAgo, err = time.ParseDuration(converted)
		if err != nil {
			s.fatal(fmt.Sprintf("Malformed prune window stop: %s %s", w, err.Error()))
		}
		newWindow.start = time.Now().Add(-timeAgo)
		windows = append(windows, newWindow)
	}
	awsAccessKeyId := os.Getenv("AWS_ACCESS_KEY_ID")
	awsSecretAccessKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if len(awsAccessKeyId) < 1 || len(awsSecretAccessKey) < 1 {
		s.fatal("Must set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.")
	}

	// connect to AWS
	auth := aws.Auth{AccessKey: awsAccessKeyId, SecretKey: awsSecretAccessKey}
	awsec2 := ec2.New(auth, sourceRegion)
	awsec2dest := ec2.New(auth, destRegion)

	// prune old AMIs and snapshots in both regions
	if len(windows) > 0 {
		err = pruneAMIs(awsec2, instanceNameTag, windows, &s)
		if err != nil {
			s.warning(fmt.Sprintf("Error pruning old AMIs: %s", err.Error()))
		}
		if destRegion.Name != sourceRegion.Name {
			err = pruneAMIs(awsec2dest, instanceNameTag, windows, &s)
			if err != nil {
				s.warning(fmt.Sprintf("Error pruning old AMIs: %s", err.Error()))
			}
		}
	}
	if arguments["--pruneonly"].(bool) {
		s.ok("Pruning done and --pruneonly specified - exiting.")
		os.Exit(s.finish())
	}

	// search for our instances
	filter := ec2.NewFilter()
	filter.Add("tag:Name", instanceNameTag)

	resp, err := awsec2.DescribeInstances(nil, filter)
	if err != nil {
		s.fatal(fmt.Sprintf("EC2 API DescribeInstances failed: %s", err.Error()))
	}
	instances := []ec2.Instance{}
	for _, reservation := range resp.Reservations {
		for _, instance := range reservation.Instances {
			instances = append(instances, instance)
		}
	}
	if len(instances) < 1 {
		s.fatal(fmt.Sprintf("No instances with matching name tag: %s", instanceNameTag))
	} else {
		s.debug(fmt.Sprintf("Found %d instances with matching Name tag: %s", len(instances), instanceNameTag))
	}

	// create local AMIs
	timeSecs := fmt.Sprintf("%d", time.Now().Unix())
	timeStamp := time.Now().Format("2006-01-02_15-04-05")
	timeString := time.Now().Format("2006-01-02 15:04:05 -0700")
	newAMIs := make(map[string]string)
	pendingAMIs := make(map[string]bool)
	for _, instance := range instances {
		backupAmiName := fmt.Sprintf("%s-%s-%s", instanceNameTag, timeStamp, instance.InstanceId)
		backupDesc := fmt.Sprintf("%s %s %s", instanceNameTag, timeString, instance.InstanceId)
		resp, err := awsec2.CreateImage(instance.InstanceId, backupAmiName, backupDesc, true)
		if err != nil {
			s.fatal(fmt.Sprintf("Error creating new AMI: %s", err.Error()))
		}
		_, err = awsec2.CreateTags([]string{resp.ImageId}, []ec2.Tag{
			{"hostname", instanceNameTag},
			{"instance", instance.InstanceId},
			{"date", timeString},
			{"timestamp", timeSecs},
		})
		if err != nil {
			s.fatal(fmt.Sprintf("Error tagging new AMI: %s", err.Error()))
		}
		newAMIs[resp.ImageId] = instance.InstanceId
		pendingAMIs[resp.ImageId] = true
		s.debug(fmt.Sprintf("Creating new AMI %s for %s (%s)", resp.ImageId, instanceNameTag, instance.InstanceId))
	}

	// wait for AMIs to be ready
	done := make(chan bool)
	go func() {
		for len(pendingAMIs) > 0 {
			s.debug(fmt.Sprintf("Sleeping for %d pending AMIs", len(pendingAMIs)))
			time.Sleep(apiPollInterval)
			list := []string{}
			for k, _ := range pendingAMIs {
				list = append(list, k)
			}
			images, err := awsec2.Images(list, nil)
			if err != nil {
				s.fatal("EC2 API Images failed")
			}
			for _, image := range images.Images {
				if image.State == "available" {
					delete(pendingAMIs, image.Id)
					s.ok(fmt.Sprintf("Created new AMI %s", image.Id))
				}
			}
		}
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		list := []string{}
		for k, _ := range pendingAMIs {
			list = append(list, k)
		}
		s.fatal(fmt.Sprintf("Timeout waiting for AMIs in region %s: %s", sourceRegion.Name, strings.Join(list, " ,")))
	}

	// start the ami copy
	if destRegion.Name != sourceRegion.Name {
		for amiId, instanceId := range newAMIs {
			backupAmiName := fmt.Sprintf("%s-%s-%s", instanceNameTag, timeStamp, amiId)
			backupDesc := fmt.Sprintf("%s %s %s", instanceNameTag, timeString, amiId)
			copyResp, err := awsec2dest.CopyImage(sourceRegion, amiId, backupAmiName, backupDesc)
			if err != nil {
				s.fatal("EC2 API CopyImage failed")
			}
			s.debug(fmt.Sprintf("Started copy of %s from %s (%s) to %s (%s).", instanceNameTag, sourceRegion.Name, amiId, destRegion.Name, copyResp.ImageId))
			_, err = awsec2dest.CreateTags([]string{copyResp.ImageId}, []ec2.Tag{
				{"hostname", instanceNameTag},
				{"instance", instanceId},
				{"sourceregion", sourceRegion.Name},
				{"date", timeString},
				{"timestamp", timeSecs},
			})
			if err != nil {
				s.fatal(fmt.Sprintf("Error tagging new AMI: %s", err.Error()))
			}
		}
	} else {
		s.debug("Not copying AMI - source and dest regions match")
	}

	os.Exit(s.finish())
}

////
func pruneAMIs(awsec2 *ec2.EC2, instanceNameTag string, windows []window, s *session) error {
	filter := ec2.NewFilter()
	filter.Add("tag:hostname", instanceNameTag)
	imageList, err := awsec2.Images(nil, filter)
	if err != nil {
		return fmt.Errorf("EC2 API Images failed: %s", err.Error())
	}
	s.debug(fmt.Sprintf("Found %d total images for %s in %s", len(imageList.Images), instanceNameTag, awsec2.Region.Name))
	images := map[string]time.Time{}
	for _, image := range imageList.Images {
		timestampTag := ""
		for _, tag := range image.Tags {
			if tag.Key == "timestamp" {
				timestampTag = tag.Value
			}
		}
		if len(timestampTag) < 1 {
			s.debug(fmt.Sprintf("AMI is missing timestamp tag - skipping: %s", image.Id))
			continue
		}
		timestamp, err := strconv.ParseInt(timestampTag, 10, 64)
		if err != nil {
			s.debug(fmt.Sprintf("AMI timestamp tag is corrupt - skipping: %s", image.Id))
			continue
		}
		images[image.Id] = time.Unix(timestamp, 0)
	}
	for _, window := range windows {
		s.debug(fmt.Sprintf("Window: 1 per %s from %s-%s", window.interval.String(), window.start, window.stop))
		for cursor := window.start; cursor.Before(window.stop); cursor = cursor.Add(window.interval) {
			imagesInThisInterval := []string{}
			imagesTimes := make(map[string]time.Time)
			for id, when := range images {
				if when.After(cursor) && when.Before(cursor.Add(window.interval)) {
					imagesInThisInterval = append(imagesInThisInterval, id)
					imagesTimes[id] = when
				}
			}
			if len(imagesInThisInterval) > 1 {
				for _, id := range imagesInThisInterval[1:] { //keep the oldest one
					resp, err := awsec2.DeregisterImage(id)
					if err != nil {
						return fmt.Errorf("EC2 API DeregisterImage failed for %s: %s", id, err.Error())
					}
					if resp.Response != true {
						return fmt.Errorf("EC2 API DeregisterImage error for %s", id)
					}
					s.debug(fmt.Sprintf("Pruned old AMI %s @ %s (%s->%s)", id, imagesTimes[id].Format("2006-01-02 15:04:05"), window.start.Format("2006-01-02 15:04:05"), window.stop.Format("2006-01-02 15:04:05")))
				}
			}
		}
	}
	return nil
}

////
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

////
func (s *session) debug(m string) {
	if s.debugMode {
		log.Println(m)
	}
}
func (s *session) fatal(m string) {
	s.errorLevel = 2
	if s.nagiosMode {
		fmt.Printf("AMIbackup CRITICAL: %s\n", m)
	} else {
		log.Println(m)
	}
	os.Exit(s.errorLevel)
}
func (s *session) warning(m string) {
	if s.errorLevel < 1 {
		s.errorLevel = 1
	}
	if s.nagiosMode {
		s.nagiosMessages = append(s.nagiosMessages, m)
		if s.debugMode {
			log.Println(m)
		}
	} else {
		log.Println(m)
	}
}
func (s *session) ok(m string) {
	if s.nagiosMode {
		s.nagiosMessages = append(s.nagiosMessages, m)
		if s.debugMode {
			log.Println(m)
		}
	} else {
		log.Println(m)
	}
}
func (s *session) finish() int {
	if s.nagiosMode {
		messages := strings.Join(s.nagiosMessages, ", ")
		if s.errorLevel < 1 {
			fmt.Printf("AMIbackup OK: %s\n", messages)
		} else if s.errorLevel < 2 {
			fmt.Printf("AMIbackup WARNING: %s\n", messages)
		} else {
			fmt.Printf("AMIbackup CRITICAL: %s\n", messages)
		}
	}
	if s.debugMode {
		log.Println("Done")
	}
	return s.errorLevel
}
