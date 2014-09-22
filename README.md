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
```

