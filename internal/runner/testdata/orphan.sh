#!/bin/sh
# Used by TestCLIRunKillsOrphanedGrandchildOnTimeout.
#
# Spawns a grandchild (a long sleep) that would be orphaned -- and keep
# running -- if the timeout path only killed this script's own process, the
# way killing just the direct child does. The grandchild's pid is written to
# orphan.pid in the current directory, which is cmd.Dir (the test's
# TempDir), so the test can find it and check whether it is still alive
# after Run() returns.
sleep 300 &
echo $! > orphan.pid
sleep 300
