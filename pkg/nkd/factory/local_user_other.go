//go:build !linux && !darwin

package factory

func localContainerUser() string { return "" }
