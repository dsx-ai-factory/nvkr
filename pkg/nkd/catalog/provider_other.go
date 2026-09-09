//go:build !linux && !darwin

package catalog

import (
	"context"
	"fmt"
)

func readCatalogRegularBeneathContext(context.Context, string, string) ([]byte, error) {
	return nil, fmt.Errorf("bounded no-follow catalog directories are unsupported on this platform")
}

func readCatalogDirectoryBeneathContext(context.Context, string, string) ([]string, error) {
	return nil, fmt.Errorf("bounded no-follow catalog directories are unsupported on this platform")
}
