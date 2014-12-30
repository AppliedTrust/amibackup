package main

import (
	"fmt"
	"github.com/crowdmob/goamz/aws"
	"github.com/crowdmob/goamz/ec2"
	"github.com/docopt/docopt-go"
	"log"
	"os"
	"strings"
	"time"
)

const version = "0.1"

var usage = `snapcleanup: clean up AWS snapshots by trying to delete ALL OF THEM

Usage:
  snapcleanup [options]
  snapcleanup -h --help
  snapcleanup --version

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
		log.Printf("Error purging snapshots: %s", err.Error())
	}
	log.Printf("Finished puring snapshots - exiting")
}

// purgeAMIs purges AMIs based on name regex
func purgeAMIs(awsec2 *ec2.EC2, s *session) error {
	filter := ec2.NewFilter()
	filter.Add("owner-id", "200691973142")
	snaps, err := awsec2.Snapshots(nil, filter)
	if err != nil {
		return fmt.Errorf("EC2 API Snapshots failed: %s", err.Error())
	}
	log.Printf("Found %d total snaps in %s", len(snaps.Snapshots), awsec2.Region.Name)
	if s.dryRun {
		log.Fatal("dryrun")
	}
	for _, s := range snaps.Snapshots {
		_, err := awsec2.DeleteSnapshots(s.Id)
		if err != nil {
			fmt.Printf("EC2 API DeleteSnapshots failed for %s: %s\n", s.Id, err.Error())
			if strings.Contains(err.Error(), "Request limit exceeded.") {
				fmt.Printf("Sleeping...\n")
				time.Sleep(time.Second * 5)
			}
			continue
		}
		log.Printf("Deleted snapshot: %s", s.Id)
	}
	return nil
}

// handleOptions parses CLI options
func handleOptions(s *session) {
	var ok bool
	arguments, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		log.Fatalf("Error parsing arguments: %s", err.Error())
	}
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
