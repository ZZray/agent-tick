//go:build !windows

package main

func systemLang() string { return envLang() }
