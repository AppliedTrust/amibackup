package main

import (
	"fmt"
	"github.com/crowdmob/goamz/aws"
	"github.com/crowdmob/goamz/ec2"
	"github.com/docopt/docopt-go"
	"log"
	"os"
	"regexp"
	"strconv"
	"time"
)

const version = "0.1"

var usage = `amicleanup: clean up old AWS AMI backups and snapshots

Usage:
  amicleanup [options] <ami_name_regex>
  amicleanup -h --help
  amicleanup --version

Options:
  -r, --region=<region>     AWS region of running instance [default: us-east-1].
  -d, --dry-run             Show what would be purged without purging it.
  -K, --awskey=<keyid>      AWS key ID (or use AWS_ACCESS_KEY_ID environemnt variable).
  -S, --awssecret=<secret>  AWS secret key (or use AWS_SECRET_ACCESS_KEY environemnt variable).
  --version                 Show version.
  -h, --help                Show this screen.

AWS Authentication:
  Either use the -K and -S flags, or
  set the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.

`

type session struct {
	dryRun             bool
	nameRegex          string
	region             aws.Region
	awsAccessKeyId     string
	awsSecretAccessKey string
}

var regionMap = map[string]aws.Region{
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

// time formatting
var timeSecs = fmt.Sprintf("%d", time.Now().Unix())
var timeStamp = time.Now().Format("2006-01-02_15-04-05")
var timeShortFormat = "01/02/2006@15:04:05"
var timeString = time.Now().Format("2006-01-02 15:04:05 -0700")

func main() {
	s := &session{}

	handleOptions(s)

	// connect to AWS
	auth := aws.Auth{AccessKey: s.awsAccessKeyId, SecretKey: s.awsSecretAccessKey}
	awsec2 := ec2.New(auth, s.region)

	// purge old AMIs and snapshots
	err := purgeAMIs(awsec2, s)
	if err != nil {
		log.Printf("Error purging old AMIs: %s", err.Error())
	}
	log.Printf("Finished puring AMIs and snapshots - exiting")
}

// findSnapshots returns a map of snapshots associated with an AMI
func findSnapshots(amiid string, awsec2 *ec2.EC2) (map[string]string, error) {
	snaps := make(map[string]string)
	resp, err := awsec2.Images([]string{amiid}, nil)
	if err != nil {
		return snaps, fmt.Errorf("EC2 API DescribeImages failed: %s", err.Error())
	}
	for _, image := range resp.Images {
		for _, bd := range image.BlockDevices {
			if len(bd.SnapshotId) > 0 {
				snaps[bd.SnapshotId] = bd.DeviceName
			}
		}
	}
	return snaps, nil
}

// purgeAMIs purges AMIs based on name regex
func purgeAMIs(awsec2 *ec2.EC2, s *session) error {
	filter := ec2.NewFilter()
	filter.Add("is-public", "false")
	imageList, err := awsec2.Images(nil, filter)
	if err != nil {
		return fmt.Errorf("EC2 API Images failed: %s", err.Error())
	}
	log.Printf("Found %d total images in %s", len(imageList.Images), awsec2.Region.Name)
	images := map[string]int{}
	r, err := regexp.Compile(s.nameRegex)
	if err != nil {
		return err
	}
	for _, image := range imageList.Images {
		if r.MatchString(image.Name) {
			log.Printf("Found: %s", image.Name)
			images[image.Id] = 0
		}
	}
	log.Printf("Found %d matching images in %s", len(images), awsec2.Region.Name)
	if s.dryRun {
		log.Fatal("dryrun")
	}
	for id, _ := range images {
		// find snapshots associated with this AMI.
		snaps, err := findSnapshots(id, awsec2)
		if err != nil {
			return fmt.Errorf("EC2 API findSnapshots failed for %s: %s", id, err.Error())
		}
		// deregister the AMI.
		resp, err := awsec2.DeregisterImage(id)
		if err != nil {
			fmt.Printf("EC2 API DeregisterImage failed for %s: %s", id, err.Error())
			time.Sleep(time.Second * 3)
			continue
		}
		if resp.Response != true {
			return fmt.Errorf("EC2 API DeregisterImage error for %s", id)
		}
		// delete snapshots associated with this AMI.
		for snap, _ := range snaps {
			_, err := awsec2.DeleteSnapshots(snap)
			if err != nil {
				fmt.Printf("EC2 API DeleteSnapshots failed for %s: %s\n", snap, err.Error())
				time.Sleep(time.Second * 3)
				continue
			}
			log.Printf("Deleted snapshot: %s (%s)", snap, id)
		}
		log.Printf("Purged old AMI %s", id)
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
func handleOptions(s *session) {
	var ok bool
	arguments, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		log.Fatalf("Error parsing arguments: %s", err.Error())
	}
	s.nameRegex = arguments["<ami_name_regex>"].(string)
	s.region, ok = regionMap[arguments["--region"].(string)]
	if !ok {
		log.Fatalf("Bad region: %s", arguments["--region"].(string))
	}
	if arguments["--dry-run"].(bool) {
		s.dryRun = true
	}
	if arg, ok := arguments["--awskey"].(string); ok {
		s.awsAccessKeyId = arg
	}
	if arg, ok := arguments["--awssecret"].(string); ok {
		s.awsSecretAccessKey = arg
	}
	// parse environment variables
	if len(s.awsAccessKeyId) < 1 {
		s.awsAccessKeyId = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if len(s.awsSecretAccessKey) < 1 {
		s.awsSecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	if len(s.awsAccessKeyId) < 1 || len(s.awsSecretAccessKey) < 1 {
		log.Fatal("Must use -K and -S options or set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.")
	}
}
