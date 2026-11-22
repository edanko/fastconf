package fastconf

// flagSet is the subset of flag.FlagSet and pflag.FlagSet needed to bind
// configuration path flags.
type flagSet interface {
	StringVar(variable *string, name string, value string, usage string)
}

// BindFlags binds the configuration path flags to a flag set.
func BindFlags(flags flagSet, configPath, bootstrapPath *string) {
	BindConfigPathFlag(flags, configPath)
	BindBootstrapPathFlag(flags, bootstrapPath)
}

// BindConfigPathFlag binds the "config_path" flag.
func BindConfigPathFlag(flags flagSet, configPath *string) {
	flags.StringVar(configPath, "config_path", "", "path to a configuration file or directory")
}

// BindBootstrapPathFlag binds the "bootstrap_path" flag.
func BindBootstrapPathFlag(flags flagSet, bootstrapPath *string) {
	flags.StringVar(bootstrapPath, "bootstrap_path", "", "path to a bootstrap configuration file")
}
