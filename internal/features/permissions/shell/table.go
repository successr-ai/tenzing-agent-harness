package shell

import (
	"maps"
	"strings"
)

// Table maps command shapes to classes. Keys take these forms:
//
//	"bin"            the binary alone
//	"bin sub"        binary plus first non-flag argument
//	"bin sub sub2"   plus the second non-flag argument (git stash list)
//	"bin -x"         binary plus a flag ("bin sub -x" also works)
//
// Lookup order: flag keys (all hits OR'd; a hit REPLACES the bin/sub
// class, so `sed -i` is a write and `unzip -l` a read), then the two-level
// subcommand key, then the subcommand key, then the bare key. Nothing
// matched is Unknown. Short flag clusters are expanded per character
// (-ni.bak → -n -i -. -b -a -k), which errs toward mutation, never toward
// read.
type Table map[string]Class

// Merge returns a new table with overrides applied over t.
func (t Table) Merge(overrides map[string]Class) Table {
	out := make(Table, len(t)+len(overrides))
	maps.Copy(out, t)
	maps.Copy(out, overrides)
	return out
}

// valueFlags are flags that consume the next argument, per binary, so the
// subcommand is found past them: `git -C dir status`.
//
// ponytail: fixed list, extend when a corpus command misclassifies.
var valueFlags = map[string]map[string]bool{
	"git":     set("-C", "-c", "--git-dir", "--work-tree", "--namespace"),
	"go":      set("-C"),
	"docker":  set("-H", "--host", "--context", "-c", "--config", "-l", "--log-level"),
	"kubectl": set("-n", "--namespace", "--context", "--kubeconfig", "-s", "--server"),
	"npm":     set("--prefix"),
	"pnpm":    set("-C", "--dir"),
	"cargo":   set("-Z", "--config"),
}

// Lookup classifies bin with its arguments per the key forms above.
func (t Table) Lookup(bin string, args []Arg) (Class, []string) {
	subs, flags := splitArgs(bin, args)
	sub, sub2 := "", ""
	if len(subs) > 0 {
		sub = subs[0]
	}
	if len(subs) > 1 {
		sub2 = subs[1]
	}

	var flagClass Class
	var why []string
	hit := false
	for _, f := range flags {
		keys := []string{bin + " " + f}
		if sub != "" {
			keys = append([]string{bin + " " + sub + " " + f}, keys...)
		}
		for _, k := range keys {
			if c, ok := t[k]; ok {
				flagClass |= c
				hit = true
				why = append(why, k+": "+c.String())
				break
			}
		}
	}
	if hit {
		return flagClass, why
	}
	if sub2 != "" {
		if c, ok := t[bin+" "+sub+" "+sub2]; ok {
			return c, []string{bin + " " + sub + " " + sub2 + ": " + c.String()}
		}
	}
	if sub != "" {
		if c, ok := t[bin+" "+sub]; ok {
			return c, []string{bin + " " + sub + ": " + c.String()}
		}
	}
	if c, ok := t[bin]; ok {
		return c, []string{bin + ": " + c.String()}
	}
	return Unknown, []string{"unknown command " + bin}
}

// splitArgs returns the static non-flag arguments in order (past
// value-taking flags) and every flag expanded into lookup tokens. A
// non-static argument in a positional slot is recorded as "\x00" so it can
// never match a key but still occupies its position.
func splitArgs(bin string, args []Arg) (subs []string, flags []string) {
	vf := valueFlags[bin]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.Static {
			subs = append(subs, "\x00")
			continue
		}
		v := a.Value
		switch {
		case v == "--":
			return subs, flags
		case strings.HasPrefix(v, "--"):
			name, _, _ := strings.Cut(v, "=")
			flags = append(flags, name)
			if vf[name] && !strings.Contains(v, "=") {
				i++
			}
		case strings.HasPrefix(v, "-") && len(v) > 1:
			for _, c := range v[1:] {
				flags = append(flags, "-"+string(c))
			}
			if vf[v] {
				i++
			}
		default:
			subs = append(subs, v)
		}
	}
	return subs, flags
}

// special handles commands whose class depends on argument shape a flat
// key cannot express. handled=false defers to the table.
type special func(argv []Arg) (Class, []string, bool)

var specials = map[string]special{
	"git":   gitSpecial,
	"tar":   tarSpecial,
	"tee":   teeSpecial,
	"awk":   awkSpecial,
	"gawk":  awkSpecial,
	"mawk":  awkSpecial,
	"rsync": rsyncSpecial,
}

// staticArgs returns the static values of args (non-static dropped) and
// whether every arg was static.
func staticArgs(args []Arg) ([]string, bool) {
	out := make([]string, 0, len(args))
	all := true
	for _, a := range args {
		if a.Static {
			out = append(out, a.Value)
		} else {
			all = false
		}
	}
	return out, all
}

func allFlags(vals []string) bool {
	for _, v := range vals {
		if !strings.HasPrefix(v, "-") {
			return false
		}
	}
	return true
}

// gitSpecial: branch/tag/stash/remote/worktree list when given nothing but
// flags (or a listing sub-subcommand), else mutate; config reads with --get
// / -l / --list, else writes.
func gitSpecial(argv []Arg) (Class, []string, bool) {
	subs, _ := splitArgs("git", argv[1:])
	if len(subs) == 0 {
		return 0, nil, false
	}
	sub := subs[0]
	rest := argv[1:]
	for i, a := range rest {
		if a.Static && a.Value == sub {
			rest = rest[i+1:]
			break
		}
	}
	vals, allStatic := staticArgs(rest)
	switch sub {
	case "branch", "tag":
		if allStatic && allFlags(vals) && !hasAny(vals, "-d", "-D", "-m", "-M", "-c", "-C", "--delete", "--move", "--copy") {
			return Read, []string{"git " + sub + " lists"}, true
		}
		return VCS, []string{"git " + sub + " changes refs"}, true
	case "stash":
		if len(vals) > 0 && (vals[0] == "list" || vals[0] == "show") {
			return Read, []string{"git stash " + vals[0]}, true
		}
		return VCS, []string{"git stash changes the working tree"}, true
	case "remote":
		if allStatic && (allFlags(vals) || vals[0] == "show" || vals[0] == "get-url") {
			return Read, []string{"git remote lists"}, true
		}
		if len(vals) > 0 && (vals[0] == "add" || vals[0] == "set-url" || vals[0] == "update") {
			return Net | VCS, []string{"git remote " + vals[0]}, true
		}
		return VCS, []string{"git remote changes config"}, true
	case "worktree":
		if len(vals) > 0 && vals[0] == "list" {
			return Read, []string{"git worktree list"}, true
		}
		return VCS | FSWrite, []string{"git worktree changes checkouts"}, true
	case "config":
		if hasAny(vals, "--get", "--get-all", "--get-regexp", "-l", "--list", "--show-origin") {
			return Read, []string{"git config read"}, true
		}
		return VCS, []string{"git config writes"}, true
	}
	return 0, nil, false
}

func hasAny(vals []string, wants ...string) bool {
	for _, v := range vals {
		for _, w := range wants {
			if v == w {
				return true
			}
		}
	}
	return false
}

// tarSpecial reads the operation letter from the first argument, which may
// omit the dash (`tar xzf a.tgz`).
func tarSpecial(argv []Arg) (Class, []string, bool) {
	if len(argv) < 2 || !argv[1].Static {
		return Unknown, []string{"tar with non-literal mode"}, true
	}
	mode := argv[1].Value
	switch {
	case mode == "--extract" || mode == "--create" || mode == "--append" || mode == "--update":
		return FSWrite, []string{"tar " + mode}, true
	case mode == "--list":
		return Read, []string{"tar --list"}, true
	}
	mode = strings.TrimLeft(mode, "-")
	if strings.ContainsAny(mode, "xcru") {
		return FSWrite, []string{"tar extracts or creates"}, true
	}
	if strings.Contains(mode, "t") {
		return Read, []string{"tar lists"}, true
	}
	return Unknown, []string{"tar with unrecognised mode"}, true
}

// teeSpecial: write unless every target is a /dev sink.
func teeSpecial(argv []Arg) (Class, []string, bool) {
	for _, a := range argv[1:] {
		if !a.Static {
			return FSWrite, []string{"tee to non-literal path"}, true
		}
		if strings.HasPrefix(a.Value, "-") {
			continue
		}
		if !devSinks[a.Value] {
			return FSWrite, []string{"tee " + a.Value}, true
		}
	}
	return Read, []string{"tee to /dev sink"}, true
}

// awkSpecial: read unless the program text redirects or shells out.
//
// ponytail: heuristic on the program string; the alternative (awk always
// Unknown) would prompt on every read pipeline.
func awkSpecial(argv []Arg) (Class, []string, bool) {
	for _, a := range argv[1:] {
		if a.Static && (strings.Contains(a.Value, ">") || strings.Contains(a.Value, "system(")) {
			return Unknown, []string{"awk program writes or runs commands"}, true
		}
	}
	return Read, []string{"awk"}, true
}

// rsyncSpecial: always a write; a remote spec adds net.
func rsyncSpecial(argv []Arg) (Class, []string, bool) {
	c := FSWrite
	why := []string{"rsync writes"}
	for _, a := range argv[1:] {
		if !a.Static {
			continue
		}
		if strings.HasPrefix(a.Value, "rsync://") || (strings.Contains(a.Value, ":") && !strings.HasPrefix(a.Value, "-")) {
			c |= Net
			why = append(why, "rsync remote "+a.Value)
			break
		}
	}
	return c, why, true
}

// Builtin is the compiled-in knowledge table. Anything not here is Unknown
// and prompts; add entries conservatively — an entry that is wrong toward
// read silently runs a mutation.
func Builtin() Table {
	t := Table{}
	add := func(c Class, keys ...string) {
		for _, k := range keys {
			t[k] = c
		}
	}

	// Shell builtins and shell-internal commands.
	add(Read, "cd", "pushd", "popd", "dirs", "echo", "printf", "pwd", "true", "false", ":", "test", "[",
		"type", "alias", "unalias", "set", "shift", "export", "declare", "typeset", "local", "readonly",
		"unset", "read", "return", "break", "continue", "exit", "wait", "jobs", "trap", "ulimit", "umask",
		"mapfile", "readarray", "hash", "help", "times", "getopts", "let", "fg", "bg", "compgen", "complete")

	// Read-only tools.
	add(Read, "ls", "dir", "cat", "head", "tail", "less", "more", "grep", "egrep", "fgrep", "rg", "ag", "ack",
		"wc", "sort", "uniq", "cut", "tr", "sed", "fd", "locate", "which", "whereis", "whatis", "file",
		"stat", "du", "df", "date", "cal", "env", "printenv", "id", "whoami", "hostname", "uname", "arch",
		"nproc", "dirname", "basename", "realpath", "readlink", "diff", "cmp", "comm", "jq", "yq", "xxd",
		"od", "hexdump", "strings", "column", "paste", "nl", "seq", "tac", "rev", "fold", "fmt", "expand",
		"unexpand", "tree", "sleep", "ps", "pgrep", "top", "htop", "lsof", "pstree", "uptime", "w", "who",
		"last", "history", "bc", "expr", "md5", "md5sum", "sha1sum", "sha256sum", "shasum", "base64",
		"tput", "tty", "stty", "getconf", "sysctl", "sw_vers", "lscpu", "free", "vmstat", "iostat",
		"netstat", "ss", "ifconfig", "route", "arp", "man", "info", "look", "join", "split", "iconv",
		"numfmt", "yes", "cksum", "sum", "gofmt", "gzip -l", "unzip -l", "zipinfo",
		"patch --dry-run", "cargo fmt --check", "zcat", "gzcat", "bzcat", "xzcat", "zless", "zgrep",
		"tsc --noEmit", "golangci-lint", "staticcheck", "shellcheck", "gofumpt -l", "make -n", "task --list")
	add(Unknown, "sysctl -w", "defaults", "ip", "gofumpt", "make", "task")
	add(Read, "ip addr", "ip link", "ip route", "ip -4", "ip -6", "ip neigh", "defaults read", "make --dry-run")

	// Flag-shaped writers on otherwise read-only tools.
	add(FSWrite, "sed -i", "sed --in-place", "sort -o", "sort --output", "gofmt -w", "gofumpt -w",
		"go env -w", "go env -u", "patch", "cargo fmt", "go fmt")

	// Build and test tools: their cache/bin output is their own workspace.
	add(Read, "go build", "go vet", "go test", "go list", "go doc", "go version", "go env", "go tool",
		"go mod graph", "go mod why", "go mod verify", "go fix",
		"cargo build", "cargo check", "cargo test", "cargo clippy", "cargo doc", "cargo tree", "cargo metadata",
		"cargo bench", "cargo --version",
		"npm ls", "npm list", "npm --version", "npm config get", "npm test", "npm t",
		"pnpm ls", "pnpm list", "yarn list", "yarn --version",
		"pip list", "pip show", "pip freeze", "pip3 list", "pip3 show", "pip3 freeze", "pip --version",
		"docker ps", "docker images", "docker logs", "docker inspect", "docker version", "docker info",
		"docker stats", "docker top", "docker port", "docker diff", "docker history", "docker --version",
		"kubectl get", "kubectl describe", "kubectl logs", "kubectl version", "kubectl explain",
		"kubectl api-resources", "kubectl api-versions", "kubectl top", "kubectl cluster-info",
		"brew list", "brew info", "brew --version", "brew doctor", "brew outdated", "brew deps", "brew leaves",
		"mise ls", "mise list", "mise current", "mise which", "mise --version",
		"terraform fmt -check", "terraform validate", "terraform show", "terraform output", "terraform version",
		"tsc", "eslint", "prettier --check", "biome check", "ruff check", "mypy", "pytest --collect-only",
		"rtk gain", "rtk --version", "rtk discover")
	add(Unknown, "go", "cargo", "npm", "pnpm", "yarn", "pip", "pip3", "docker", "kubectl", "brew", "mise",
		"terraform", "prettier", "biome", "ruff", "pytest", "rtk", "gh", "aws", "gcloud", "az", "ansible",
		"ansible-playbook", "npx", "bunx", "go generate", "go run", "cargo run", "npm run", "npm exec",
		"npm start", "pnpm run", "pnpm exec", "yarn run", "docker run", "docker exec", "docker compose",
		"docker-compose", "kubectl exec", "kubectl config", "brew services", "just")

	// Filesystem writers.
	add(FSWrite, "cp", "mv", "mkdir", "touch", "chmod", "chown", "chgrp", "ln", "install", "dd", "truncate",
		"gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz", "zip", "unzip", "mkfifo", "mknod", "mktemp",
		"go mod vendor", "go mod edit", "go work",
		"cargo update", "cargo generate-lockfile", "npm pkg set", "npm version", "npm init",
		"docker build", "docker save", "docker load", "docker cp",
		"prettier --write", "biome format", "ruff format", "black", "isort", "swag init",
		"virtualenv", "uv venv")
	add(FSWrite|Net, "npm install", "npm i", "npm ci", "npm add", "npm update", "npm up", "npm uninstall",
		"npm link", "npm publish", "npm dedupe",
		"pnpm install", "pnpm i", "pnpm add", "pnpm update", "pnpm remove", "pnpm publish",
		"yarn", "yarn install", "yarn add", "yarn remove", "yarn upgrade", "yarn publish",
		"pip install", "pip3 install", "pip download", "pip3 download", "pip uninstall", "pip3 uninstall",
		"uv pip install", "uv sync", "uv add", "poetry install", "poetry add", "pipx install",
		"go get", "go install", "go mod download", "go mod tidy",
		"cargo add", "cargo install", "cargo publish", "cargo fetch", "cargo remove",
		"brew install", "brew upgrade", "brew update", "brew tap", "brew uninstall", "brew reinstall",
		"apt", "apt-get", "dnf", "yum", "pacman", "apk", "snap", "flatpak",
		"docker pull", "docker push", "gem install", "bundle install", "composer install", "mise install",
		"mise use", "rustup", "nvm install", "wget", "git clone", "gh repo clone", "git submodule")
	add(Net, "curl", "ssh", "scp", "sftp", "nc", "ncat", "netcat", "telnet", "ping", "ping6", "dig", "nslookup",
		"host", "traceroute", "mtr", "whois", "openssl s_client", "gh", "docker login", "docker logout",
		"docker search", "npm view", "npm info", "npm audit", "npm outdated", "npm search", "npm whoami",
		"npm login", "pip index", "cargo search", "cargo login", "brew search", "brew fetch",
		"kubectl apply", "kubectl delete", "kubectl create", "kubectl patch", "kubectl edit",
		"kubectl port-forward", "kubectl rollout", "kubectl scale", "kubectl label", "kubectl annotate",
		"kubectl cp", "kubectl drain", "kubectl cordon", "kubectl uncordon", "kubectl taint", "kubectl replace",
		"kubectl set", "kubectl expose", "kubectl run", "kubectl attach", "kubectl proxy",
		"terraform plan", "terraform init", "terraform apply", "terraform destroy", "terraform import",
		"terraform refresh", "terraform state", "gh api", "git ls-remote", "git fetch", "git remote add",
		"git remote set-url", "git remote update", "git remote prune", "aws s3 ls", "aws sts get-caller-identity")
	add(FSWrite|Net, "curl -o", "curl -O", "curl --output", "curl --remote-name", "curl --output-dir")

	// Deleters.
	add(FSDelete, "rm", "rmdir", "unlink", "shred", "trash", "go clean", "cargo clean", "npm cache clean",
		"docker rm", "docker rmi", "docker image prune", "docker container prune", "docker system prune",
		"docker volume rm", "docker volume prune", "docker network rm", "brew cleanup", "git clean")
	t["git clean"] = FSDelete | VCS

	// Version control.
	add(Read, "git status", "git log", "git diff", "git show", "git blame", "git rev-parse", "git rev-list",
		"git ls-files", "git ls-tree", "git describe", "git cat-file", "git name-rev", "git shortlog",
		"git reflog", "git grep", "git count-objects", "git fsck", "git --version", "git --help", "git help",
		"git var", "git check-ignore", "git check-attr", "git merge-base", "git for-each-ref", "git show-ref",
		"git diff-tree", "git diff-index", "git diff-files", "git whatchanged", "git range-diff",
		"git cherry", "git log --oneline", "git bisect log", "git bisect view", "git show-branch",
		"git verify-commit", "git verify-tag", "git symbolic-ref --short", "git annotate", "git archive")
	add(VCS, "git commit", "git add", "git rm", "git mv", "git reset", "git checkout", "git switch",
		"git restore", "git rebase", "git merge", "git cherry-pick", "git revert", "git apply", "git am",
		"git notes", "git replace", "git filter-branch", "git update-ref", "git symbolic-ref", "git gc",
		"git prune", "git repack", "git init", "git bisect", "git mergetool", "git rerere", "git update-index",
		"git read-tree", "git write-tree", "git commit-tree", "git pack-refs", "git stash",
		"git format-patch", "git bundle", "git sparse-checkout", "git lfs", "git maintenance")
	add(Net|VCS, "git push", "git pull", "git svn", "git request-pull", "git send-email")
	add(Unknown, "git branch", "git tag", "git remote", "git worktree", "git config") // gitSpecial decides

	// Unknown by design: interactive, arbitrary code, or system control.
	add(Unknown, "su", "kill", "pkill", "killall", "systemctl", "launchctl", "service", "crontab",
		"mount", "umount", "chroot", "vim", "vi", "nvim", "nano", "emacs", "code", "subl", "open", "xdg-open",
		"osascript", "python", "python3", "python2", "node", "deno", "bun", "ruby", "perl", "php", "lua",
		"Rscript", "julia", "java", "source", ".", "bash", "sh", "zsh", "dash", "ksh", "fish", "screen", "tmux",
		"ssh-keygen", "ssh-add", "gpg", "passwd", "chsh", "reboot", "shutdown", "halt", "dscl", "pmset",
		"diskutil", "hdiutil", "fdisk", "mkfs", "iptables", "pfctl", "nft", "ufw", "sqlite3", "psql", "mysql",
		"redis-cli", "mongo", "mongosh", "expect", "script", "watch", "at", "batch", "disown")
	add(FSWrite|Unknown, "perl -i", "perl -pi")
	add(FSWrite, "sudoedit")

	return t
}
