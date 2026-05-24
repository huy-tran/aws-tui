package ec2

import "os/exec"

// execAWS builds an *exec.Cmd that invokes the local `aws` CLI. We shell out
// because session-manager-plugin and the SSM data-channel protocol are not
// reimplemented in this tool (per spec).
func execAWS(args ...string) *exec.Cmd {
	return exec.Command("aws", args...)
}
