package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

func TestPrintOut_HasTimestampPrefixAndNewline(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printOut(cmd, "hello %s\n", "world")

	assert.Regexp(t, `^\[\d{2}:\d{2}:\d{2}\] hello world\n$`, buf.String())
}

func TestPrintErr_HasTimestampPrefix(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&buf)

	printErr(cmd, "boom %d\n", 7)

	assert.Regexp(t, `^\[\d{2}:\d{2}:\d{2}\] boom 7\n$`, buf.String())
}
