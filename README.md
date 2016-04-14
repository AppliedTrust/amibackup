##amibackup: create cross-region AWS AMI backups

```
Usage:
  amibackup [options] [-p <window>]... <instance_name_tag>
  amibackup -h --help
  amibackup --version

Options:
  -s, --source=<region>     AWS region of running instance [default: us-east-1].
  -d, --dest=<region>       AWS region to store backup AMI [default: us-west-1].
  -t, --timeout=<time>      Timeout waiting for AMI creation [default: 30m]. Uses Go-style time specifier (ex: 10s, 1m, 2h)
  -p, --purge=<window>      Comma-separated list of purge windows - see below for details.
  -n, --nagios              Run like a Nagios check.
  -o, --purgeonly           Purge old AMIs without creating new ones.
  -h, --help                Show this screen.

Purge windows:
  Delete old AMIs (and associated snapshots) based on the Purge windows you define.
  By default, no AMIs are purged.  AMIs within Purge Windows are purged.
  Format is: PURGE_INTERVAL:PURGE_START:PURGE_END
  Each is a time interval (second/minute/hour/day), such as: 1s:4m:9d
  Where:
    PURGE_INTERVAL    time interval in which to keep one backup
    PURGE_START       start purging (ago)
    PURGE_END         end purging (ago)
  Sample purge schedule:
  -p 1d:4d:30d -p 7d:30d:90d -p 30d:90d:180d   Keep all for past 4 days, 1/day for past 30 days, 1/week for past 90 days, 1/mo forever.
```
