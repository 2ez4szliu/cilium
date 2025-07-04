<!-- This file was autogenerated via cilium-health cmdref, do not edit manually-->

## cilium-health completion zsh

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(cilium-health completion zsh)

To load completions for every new session, execute once:

#### Linux:

	cilium-health completion zsh > "${fpath[1]}/_cilium-health"

#### macOS:

	cilium-health completion zsh > $(brew --prefix)/share/zsh/site-functions/_cilium-health

You will need to start a new shell for this setup to take effect.


```
cilium-health completion zsh [flags]
```

### Options

```
  -h, --help              help for zsh
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
  -D, --debug         Enable debug messages
  -H, --host string   URI to cilium-health server API
```

### SEE ALSO

* [cilium-health completion](cilium-health_completion.md)	 - Generate the autocompletion script for the specified shell

