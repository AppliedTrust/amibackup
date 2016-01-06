![Supported](https://img.shields.io/badge/development_status-supported-brightgreen.svg) ![License BSDv2](https://img.shields.io/badge/license-BSDv2-brightgreen.svg)
##amibackup: create cross-region AWS AMI backups

```
Usage:
  amibackup [options] [-p <window>]... <instance_name_tag>
  amibackup -h --help
  amibackup --version

Options:
  -s, --source=<region>     AWS region of running instance [default: us-east-1].
  -d, --dest=<region>       AWS region to store backup AMI [default: us-west-1].
  -t, --timeout=<secs>      Timeout waiting for AMI creation [default: 300].
  -p, --purge=<window>      Comma-separated list of purge windows - see below for details.
  -n, --nagios              Run like a Nagios check.
  -o, --purgeonly           Purge old AMIs without creating new ones.
  --debug                   Enable debugging output.
  --version                 Show version.
  -h, --help                Show this screen.

Purge Windows:
  Delete old AMIs (and associated snapshots) based on the Purge Windows you define.
  If no Purge Windows are defined, nothing will be purged... ie:  AMIs are only purged if they are within Purge Windows.
  Thus, AMIs older than your oldest Purge Window will be kept forever.

  Format is: PURGE_INTERVAL:PURGE_START:PURGE_END
  Where:
    PURGE_INTERVAL    time interval in which to keep one backup
    PURGE_START       start purging (ago)
    PURGE_END         end purging (ago)
  Each is a time interval (second/minute/hour/day), such as: 1s:4m:9d

  Sample purge schedule:
  -p 1d:1d:7d                                  Keep 1/day forever.
  -p 1d:1h:7d  -p 7d:7d:14d                    Keep 1/day for past 7 days, 1/week forever.
  -p 1d:4d:30d -p 7d:30d:90d -p 30d:90d:180d   Keep all for past 4 days, 1/day for past 30 days, 1/week for past 90 days, 1/mo forever.
```



