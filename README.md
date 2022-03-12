# OP
Putting the "op" in "devops"

Corny slogans aside, op aims to facilitate automation of repetitive workflows in a (hopefully) intuitive, clear and convenient way.
As it is right now, it can mostly be viewed as a presumptuous scripting alternative.

# Overview
The core working concept is that of "route". A route is a set of commands (called "procs") to be executed in sequential order. Routes are asynchronous to other routes.

Routes are defined inside yaml manifest files, typically named "op.yaml", that are placed where needed. An effectively written manifest file should allow execution of common workflows by simply executing op in the appropriate working directory, while also providing more situational workflows accessible through one or two arguments.

Op is designed to centralize route monitoring and control. The first launched op process will act as server for subsequent op processes launched during its lifetime. An op process exits when all its routes have reached completion. By detaching from an op command, or by using a different console, active routes can be listed, restarted or terminated through use of dedicated flags.

# Manifest structure
When starting, op reads the manifest "op.yaml" in the working directory. A valid manifest is required to do anything.
A different file may be provided through an OP env.
The -g flag overrides this default behaviour, using an OP\_GLOBAL env instead. This streamlines usage of a session master config.
-g is currently the only flag that may be present alongside others.

A manifest file is structed in three distinct layers:\
top - the default outermost layer\
routes - route definitions\
procs - individual proc definitions inside each route\
In general, each layer rolls out attributes to nested ones. Conflicts between layers are defered to the lower one. This behaviour is analogous to scopes in programming languages.\
Attributes that are rolled out are mentioned explicitly in this documentation.

Routes are defined as a map inside a top "routes" attribute.\
Procs are defined as arrays inside the "procs" attribute of each route.

A very simple manifest file looks like this:
```text
routes:
  a:
    default: true
    procs:
    - path: echo
      args: [Hello there]
      out: std
```

Proc attributes. Only the path attribute is mandatory:
```text
name - proc name; defaults to its index in its parent route, starting from 0
path - executable path; may be relative to working directory
dir - process working directory; may be relative; defaults to inherited
env - process environment variables as a map; must be defined explicitly, nothing is inherited
args - process args as a string array
in - stdin file
out - stdout file; truncated if exists; special value "std" inherits; defaults to /dev/null
err - stderr file; truncated if exists; special value "std" inherits; defaults to /dev/null
```

A route may have a "default" bool attribute to indicate if it should be run when executing op without arguments. This defaults to false.

Env expansion\
At any point in the manifest file, env markers may be placed, of the form ${NAME}. The manifest file will be preprocessed to replace each such marker with the value of the corresponding env, as seen by the op program itself.
To keep the literal "${string}" in the file, it must be escaped using a backslash ("\\${string}").

Env declaration\
Each route, as well as the top layer, may have their own "env" attribute, similar to the proc one. This is rolled out to nested layers, by stacking them together. Lower level keys take priority.

Convenience variables\
Any level of the configuration (top/route/proc) may have a "var" attribute. This must be a string map, similar to "env".
These variables may then be referenced in any string attribute within the same scope, using the Go template syntax. Notably, in the context of yaml, they must be used inside quoted strings.
As with envs, inner var declarations stack with and have priority over higher level ones.
Vars are evaluated and applied after env expansion.

Namespaces\
In order to allow route declarations without having to worry about potential name conflicts with other manifests, a namespace feature is used.\
Each manifest may have a top layer "namespace" attribute for this purpose, and each route may redefine this attribute. If absent, this defaults to the "default" namespace.\
All route operations are executed within the context of the appropriate namespace.

Example op.yaml file:
```text
namespace: someNS
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
      args: [somearg, anotherarg, "this is {{.a}}"]
      env:
        HOME: someotherpath
        FOO: bar
      out: std
      err: error.log
  route1:
    procs:
    - path: otherprogram
```

# Execution mode
With no arguments, runs all default routes in the manifest file. Waits for them to finish.\
With one argument, runs only that route.\
With two arguments, runs only specific proc in route.\
In all these cases, automatically functions as a server, if none already running.
Any additional op programs will function as clients to that server.

A few special flags are recognized. They must be placed before the actual arguments:
```text
-g -> use manifest file specified by the OPGLOBAL env
-p -> print manifest file routes
-l -> list active routes of a running server
-k -> kill active routes; may specify route as additional argument
-r -> restart all routes; may specify route as additional argument; may use different config file
-s -> start as dedicated server; does not run anything; only exits on fatal error
-e -> shuts down dedicated server; otherwise functions as -k with no arguments
-m -> generate config file; see meta structure below
```
Any values after these flags are interpreted as actual arguments. Flags may not be combined with other flags, with the expection of the global "-g" flag.

# Meta structure
Meta mode generates a new config file. It applies the specified variant found in "op\_meta.yaml" to the template found in "op\_template.yaml".
Different files may be provided through OP\_META and OP\_TEMPLATE envs. Resulting config will be written to "op.yaml" or the value of the OP env.

A meta file must contain a "variants" map member that contains individual variant definitions.
It should also contain an "active" string memeber that names the currently active variant.

Each variant may define any number of key-value pairs to be used in templates. Example:\
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

# Environment variables
Op itself uses the following envs:
```text
OP - manifest file path
OP_GLOBAL - global manifest path, when using the -g flag
OP_META - template variant file path; used with the -m flag
OP_TEMPLATE - template file path; used with the -m flag
OP_PORT - local port used by servers to communicate with new clients; defaults to :2048
OP_WORKDIR - directory used for temporary files required throughtout op's lifecycle; read/write access to it is required; defaults to /run/user/[uid]/op which will be created if it does not exist
```

# Disclaimer
I've been using op since I wrote its first version, but that is in no way a guarantee that it doesn't have bugs, especially in use cases that I rarely touch upon. Feel free to play around with it, but don't place it in any critical pipelines.

Currently only supports Linux. Possibly also Mac, but I don't have one to test.

# Development
This is a project I have embarked on, first and foremost, for personal use. It is now public because it seemed like a cool idea.

As the versioning tag would suggest, nothing in this repo should be considered stable. I tend to not hesitate cutting things out and redesigning from scratch, if I conclude that would lead to better user or coding experience in the long term.

Naturally, I will be gravitating towards features and fixes that I feel a personal need for. That being said, feature and especially bugfix requests are not excluded, as time permits.

This is my first public repo, and as such I can't quite pronounce myself on the issue of contributions. You can expect that I will be fussy on this subject.

Current\
Proc input files, as well as namespaces, are the very recently added. Finding and fixing any bugs that this might have surfaced is the current priority.

Future
- Provide more options for proc stdin and stdout linkage, other than temporary regular files\
- Inter-route coordonation\
- The ability for op to propagate itself and execute routes on other hosts
