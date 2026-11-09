// Package config is a fixture: a config file found in the tree.
package config

import "os"

// Load reads the registry from a file the tree under test controls.
func Load() ([]byte, error) { return os.ReadFile(".avalrc") }
