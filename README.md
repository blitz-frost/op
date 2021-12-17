# Execution mode
With no arguments, runs all routes in the config file. Waits for them to finish.
With one argument, runs only that route.
With two arguments, runs only specific process in route.
In all these cases, automatically functions as a server, if none already running.
Any additional op programs will function as clients to that server.

A few special flags are recognized:
```text
-g -> use manifest specified by the OPGLOBAL env
-p -> print config file routes
-l -> list active routes of a running server
-k -> kill active routes; may specify route as additional argument
-r -> restart all routes; may specify route as additional argument; may use different config file
-s -> start as dedicated server; does not run anything; only exits on fatal error
-e -> shuts down dedicated server; otherwise functions as -k with no arguments
-m -> generate config file; see meta structure below
```
Any space separated values after these flags are interpreted as actual inputs.

# Config options
Read from "op.yaml" from working directory.
A different file may be provided through an OP env.
The -g flag overrides this default behaviour, using an OPGLOBAL env instead. This streamlines usage of a session master config.
-g is currently the only flag that may be present alongside others.

The config file contains routes under the "routes" top level attribute.

Global env may be defined in a top level "env" attribute. Each route may have its own "env" attribute.
Process envs are merged with the route and global ones; innermost values take precedence.

Process attributes:
```text
name - process name; defaults to # in route, starting from 0
path - executable path; may be relative to working directory
dir - process working directory; may be relative; defaults to inherited
env - process env; must be defined explicitly, nothing is inherited
args - process args; optional
out - stdout target file; "std" inherits; defaults to /dev/null
err - stderr target file; "std" inherits; defaults to /dev/null

A route may have a "default" bool attribute to indicate if it should be run when running op without arguments. This defaults to false.
```

At any point in the configuration file. Env markers may be placed, of the form ${NAME}. File will be preprocessed to replace each such marker with the value of the corresponding env, as seen by the op program itself.
To keep the literal "${string}" in the file, it must be escaped using a backslash ("\\${string}").

Any level of the configuration (top/route/process) may have a "var" field. This must be a string value map, similar to "env".
These variables may then be referenced in any string field within the same scope, using the Go template syntax. Notably, in the context of yaml, they must be used inside quoted strings.
As with envs, inner var declarations have priority over higher level ones.
Vars are evaluated and applied after env expansion.

Example op.yaml file:
```text
var:
  a: somestring
env:
  HOME: ${HOME}
  A: "{{.a}}"
routes:
  route0:
    default: true
    env:
      HOME: somepath
    procs:
    - path: someprogram
      args: [somearg, anotherarg, "a={{.a}}"]
      env:
        HOME: someotherpath
        FOO: bar
      out: std
      err: error.log
  route1:
    procs:
    - path: otherprogram
```

# Meta structure
Meta mode generates a config file. It applies the specified variant found in "op\_meta.yaml" to the template found in "op\_template.yaml".
Different files may be provided through OP\_META and OP\_TEMPLATE envs. Resulting config will be written to "op.yaml" or the value of the OP env.

A meta file must contain a "variants" map member that contains individual variant definitions.
It should also contain an "active" string memeber that names the currently active variant.

Each variant may define any number of key-value pairs to be used in templates.
Example:
Meta file
```text
active: foo
variants:
  foo:
    some: somefoo
    other: otherfoo
  bar:
    some: somebar
    other: otherbar
```
Template file
```text
routes:
  route0:
    procs:
    - path: echo
      args: [{{.some}}]
```
Calling "op -m bar" will write out a new configuration
```text
routes:
  route0:
    procs:
    - path: echo
      args: [somebar]
```

# Requirements
Local port :2048
Read/Write access to /run/user/[uid]
