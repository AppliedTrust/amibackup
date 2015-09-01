package main

import (
	"fmt"
	"github.com/docopt/docopt-go"
	"github.com/dustin/go-humanize"
	"github.com/mitchellh/goamz/aws"
	"github.com/mitchellh/goamz/ec2"
	"html/template"
	"log"
	"os"
	"sort"
	"strconv"
	"time"
)

const version = "0.1"

var usage = `amiinventory: show AMIs created with amibackup 
Usage:
  amiinventory [options] <instance_name_tag>
  amiinventory -h --help
  amiinventory --version

Options:
  -s, --source=<region>     AWS region of running instance [default: us-east-1].
  -d, --dest=<region>       AWS region where backup AMIs are stored [default: us-west-1].
  -K, --awskey=<keyid>      AWS key ID (or use AWS_ACCESS_KEY_ID environemnt variable).
  -S, --awssecret=<secret>  AWS secret key (or use AWS_SECRET_ACCESS_KEY environemnt variable).
  --version                 Show version.
  -h, --help                Show this screen.

AWS Authentication:
  Either use the -K and -S flags, or
  set the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.
`

type session struct {
	InstanceNameTag    string
	SourceRegion       aws.Region
	DestRegion         aws.Region
	auth               aws.Auth
	awsAccessKeyId     string
	awsSecretAccessKey string
}
type ami struct {
	Id           string
	Region       string
	When         time.Time
	Relative     string
	Name         string
	InstanceId   string
	InstanceName string
}
type amiList []ami

func (t amiList) Len() int {
	return len(t)
}
func (t amiList) Less(i, j int) bool {
	return t[j].When.Before(t[i].When)
}
func (t amiList) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
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

func main() {
	s := handleOptions()

	// search for our instances
	instances, err := s.findInstances(s.SourceRegion)
	if err != nil {
		log.Fatalf("EC2 API DescribeInstances failed: %s", err.Error())
	} else if len(instances) < 1 {
		log.Fatalf("No instances with matching name tag: %s", s.InstanceNameTag)
	} else if len(instances) > 1 {
		log.Printf("Warning: Found %d instances with matching Name tag: %s", len(instances), s.InstanceNameTag)
	}

	sourceAmis, err := s.findAMIs(s.SourceRegion)
	if err != nil {
		log.Fatalf("EC2 API FindAMIs failed: %s", err.Error())
	}
	destAmis, err := s.findAMIs(s.DestRegion)
	if err != nil {
		log.Fatalf("EC2 API FindAMIs failed: %s", err.Error())
	}

	tSrc := template.New("report")
	templateText, err := Asset("static/index.html")
	if err != nil {
		log.Fatalf("Error loading html template: %s", err.Error())
	}
	t, err := tSrc.Parse(string(templateText))
	if err != nil {
		log.Fatalf("Error parsing html template: %s", err.Error())
	}

	sort.Sort(sourceAmis)
	sort.Sort(destAmis)
	data := struct {
		Instances   []ec2.Instance
		Session     *session
		Now         time.Time
		SourceAmis  *amiList
		DestAmis    *amiList
		SourceCount int
		DestCount   int
	}{
		instances,
		s,
		time.Now(),
		sourceAmis,
		destAmis,
		len(*sourceAmis),
		len(*destAmis),
	}
	err = t.Execute(os.Stdout, data)
	if err != nil {
		log.Fatal(err)
	}

}

// findInstances searches for our instances
func (s *session) findInstances(region aws.Region) ([]ec2.Instance, error) {
	aws := ec2.New(s.auth, region)
	instances := []ec2.Instance{}
	filter := ec2.NewFilter()
	filter.Add("tag:Name", s.InstanceNameTag)
	resp, err := aws.Instances(nil, filter)
	if err != nil {
		return instances, err
	}
	for _, reservation := range resp.Reservations {
		for _, instance := range reservation.Instances {
			instances = append(instances, instance)
		}
	}
	return instances, nil
}

// findAMIs finds AMIs for a given instance name tag
func (s *session) findAMIs(region aws.Region) (*amiList, error) {
	aws := ec2.New(s.auth, region)
	images := amiList{}
	filter := ec2.NewFilter()
	filter.Add("tag:hostname", s.InstanceNameTag)
	imageList, err := aws.Images(nil, filter)
	if err != nil {
		return &images, fmt.Errorf("EC2 API Images failed: %s", err.Error())
	}
	for _, image := range imageList.Images {
		thisImage := ami{Id: image.Id, Region: aws.Region.Name, Name: image.Name}
		timestampTag := ""
		for _, tag := range image.Tags {
			if tag.Key == "instance" {
				thisImage.InstanceId = tag.Value
			} else if tag.Key == "hostname" {
				thisImage.InstanceName = tag.Value
			} else if tag.Key == "timestamp" {
				timestampTag = tag.Value
			}
		}
		if len(timestampTag) < 1 {
			// log.Printf("AMI is missing timestamp tag - skipping: %s", image.Id)
			continue
		}
		timestamp, err := strconv.ParseInt(timestampTag, 10, 64)
		if err != nil {
			// log.Printf("AMI timestamp tag is corrupt - skipping: %s", image.Id)
			continue
		}
		thisImage.When = time.Unix(timestamp, 0)
		thisImage.Relative = humanize.Time(thisImage.When)
		images = append(images, thisImage)
	}
	return &images, nil
}

// handleOptions parses CLI options
func handleOptions() *session {
	var ok bool
	s := session{}
	arguments, err := docopt.Parse(usage, nil, true, version, false)
	if err != nil {
		log.Fatalf("Error parsing arguments: %s", err.Error())
	}
	s.InstanceNameTag = arguments["<instance_name_tag>"].(string)
	s.SourceRegion, ok = regionMap[arguments["--source"].(string)]
	if !ok {
		log.Fatalf("Bad region: %s", arguments["--source"].(string))
	}
	s.DestRegion, ok = regionMap[arguments["--dest"].(string)]
	if !ok {
		log.Fatalf("Bad region: %s", arguments["--dest"].(string))
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
		log.Fatalf("Must use -K and -S options or set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables.")
	}
	s.auth = aws.Auth{AccessKey: s.awsAccessKeyId, SecretKey: s.awsSecretAccessKey}
	return &s
}
