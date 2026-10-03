//go:build !linux

package onboarding

import "fmt"

func mountedToken(string) ([]byte, error) {
	return nil, fmt.Errorf("mounted-token mode is only supported inside Linux containers")
}
