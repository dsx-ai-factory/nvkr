//go:build !darwin && !linux

package pipeline

func publishTransportNoReplace(_, _ string) error {
	return errTransportNoReplaceUnsupported
}

func publishTransportAtNoReplace(_ int, _ string, _ int, _ string) error {
	return errTransportNoReplaceUnsupported
}
