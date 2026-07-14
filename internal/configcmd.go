package internal

import (
	"fmt"
	"os"
)

// CmdConfig manages the standalone config file (~/.config/swatter/config.json)
// so a user can trial Swatter without exporting SWATTER_* by hand. The file is
// layered under the environment (applyConfigFileDefaults), so it never changes
// CI behavior.
func CmdConfig(args []string) int {
	if len(args) == 0 {
		configUsage()
		return 2
	}
	switch args[0] {
	case "set":
		return configSet(args[1:])
	case "get":
		return configGet(args[1:])
	case "list", "ls":
		return configList()
	case "path":
		path, err := ConfigFilePath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "swatter config: %v\n", err)
			return 1
		}
		fmt.Println(path)
		return 0
	case "-h", "--help", "help":
		configUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "swatter config: unknown subcommand %q\n\n", args[0])
		configUsage()
		return 2
	}
}

func configSet(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: swatter config set <key> <value>")
		return 2
	}
	k, ok := lookupConfigKey(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "swatter config: unknown key %q (known: %s)\n", args[0], knownConfigKeys())
		return 2
	}
	m := loadConfigFile()
	if m == nil {
		m = map[string]string{}
	}
	m[k.name] = args[1]
	if err := saveConfigFile(m); err != nil {
		fmt.Fprintf(os.Stderr, "swatter config: %v\n", err)
		return 1
	}
	path, _ := ConfigFilePath()
	shown := args[1]
	if k.secret {
		shown = "(hidden)"
	}
	fmt.Printf("set %s = %s  (%s)\n", k.name, shown, path)
	return 0
}

func configGet(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: swatter config get <key>")
		return 2
	}
	k, ok := lookupConfigKey(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "swatter config: unknown key %q (known: %s)\n", args[0], knownConfigKeys())
		return 2
	}
	// get retrieves a single explicitly-named value, so it prints the real value
	// (unlike list, which redacts secrets across the whole file).
	v, ok := loadConfigFile()[k.name]
	if !ok || v == "" {
		fmt.Fprintf(os.Stderr, "swatter config: %s is not set\n", k.name)
		return 1
	}
	fmt.Println(v)
	return 0
}

func configList() int {
	m := loadConfigFile()
	path, _ := ConfigFilePath()
	if len(m) == 0 {
		fmt.Printf("no config set (%s)\n", path)
		return 0
	}
	fmt.Printf("# %s\n", path)
	for _, k := range configKeys {
		if v, ok := m[k.name]; ok && v != "" {
			fmt.Printf("%-14s %s\n", k.name, redactConfigValue(k, v))
		}
	}
	return 0
}

func configUsage() {
	fmt.Fprintf(os.Stderr, `swatter config — manage standalone config (~/.config/swatter/config.json)

Usage:
  swatter config set <key> <value>   Store a value (env still overrides it)
  swatter config get <key>           Print one value
  swatter config list                Show all set keys (secrets redacted)
  swatter config path                Print the config file path

Keys: %s

The file is read only when the matching SWATTER_* env var is unset, so CI
behavior (which sets the env) is unchanged. Secrets are written 0600.
`, knownConfigKeys())
}
