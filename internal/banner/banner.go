package banner

import (
	"fmt"
	"strings"
)

const logo = `
======================================================================
 ____          _ _       _     _                         _
/ ___|_      _(_) |_ ___| |__ | |__   ___   __ _ _ __ __| |
\___ \ \ /\ / / | __/ __| '_ \| '_ \ / _ \ / _` + "`" + ` | '__/ _` + "`" + ` |
 ___) \ V  V /| | || (__| | | | |_) | (_) | (_| | | | (_| |
|____/ \_/\_/ |_|\__\___|_| |_|_.__/ \___/ \__,_|_|  \__,_|
----------------------------------------------------------------------`

const footer = `======================================================================`

// ConfigLine represents a single configuration line to display
type ConfigLine struct {
	Label string
	Value string
}

// Print displays the startup banner with the service name and configuration
func Print(serviceName string, config []ConfigLine) {
	fmt.Println(logo)
	fmt.Printf("%s\n", serviceName)

	// Find max label length for alignment
	maxLen := 0
	for _, c := range config {
		if len(c.Label) > maxLen {
			maxLen = len(c.Label)
		}
	}

	// Print config lines with alignment
	for _, c := range config {
		padding := strings.Repeat(" ", maxLen-len(c.Label))
		fmt.Printf("  %s%s : %s\n", c.Label, padding, c.Value)
	}

	fmt.Println()
	fmt.Println("Ready.")
	fmt.Println(footer)
	fmt.Println()
}
