//go:build !darwin

package ui

func prepareNativeWindowTitle(string) func() { return func() {} }
