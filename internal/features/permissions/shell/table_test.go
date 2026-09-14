package shell

import (
	"strings"
	"testing"
)

// Every key must be one of the documented shapes, or it can never match.
func TestBuiltinKeysWellFormed(t *testing.T) {
	for k := range Builtin() {
		f := strings.Fields(k)
		switch {
		case len(f) == 0 || len(f) > 3 || strings.Join(f, " ") != k:
			t.Errorf("malformed key %q", k)
		case len(f) == 3 && strings.HasPrefix(f[1], "-"):
			t.Errorf("key %q: a flag cannot precede a subcommand", k)
		case len(f) >= 2 && strings.HasPrefix(f[0], "-"):
			t.Errorf("key %q: binary looks like a flag", k)
		}
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		command string
		want    Class
	}{
		{`sed -n 1p f`, Read},
		{`sed -i 's/a/b/' f`, FSWrite},
		{`sed -ni 's/a/b/p' f`, FSWrite},
		{`sed --in-place=.bak 's/a/b/' f`, FSWrite},
		{`sed -e 's/a/b/' f`, Read},
		{`git status`, Read},
		{`git push origin main`, Net | VCS},
		{`git foo`, Unknown},
		{`git -C dir log --oneline`, Read},
		{`git branch`, Read},
		{`git branch -a`, Read},
		{`git branch new`, VCS},
		{`git branch -d old`, VCS},
		{`git tag`, Read},
		{`git tag v1`, VCS},
		{`git stash list`, Read},
		{`go mod tidy`, FSWrite | Net},
		{`npm cache clean --force`, FSDelete},
		{`docker image prune -f`, FSDelete},
		{`git stash`, VCS},
		{`git remote -v`, Read},
		{`git remote add o url`, Net | VCS},
		{`git config --get user.name`, Read},
		{`git config user.name x`, VCS},
		{`git worktree list`, Read},
		{`git clean -fd`, FSDelete | VCS},
		{`git clone url`, FSWrite | Net},
		{`tar tzf a.tgz`, Read},
		{`tar -tzf a.tgz`, Read},
		{`tar xzf a.tgz`, FSWrite},
		{`tar -czf a.tgz dir`, FSWrite},
		{`tee out`, FSWrite},
		{`tee /dev/null`, Read},
		{`tee -a /dev/stderr`, Read},
		{`curl https://x`, Net},
		{`curl -sSLo f https://x`, FSWrite | Net},
		{`curl -o f https://x`, FSWrite | Net},
		{`curl -of https://x`, FSWrite | Net},
		{`curl --output f https://x`, FSWrite | Net},
		{`curl --output=f https://x`, FSWrite | Net},
		{`curl -O https://x/f`, FSWrite | Net},
		{`curl -sO https://x/f`, FSWrite | Net},
		{`curl --remote-name https://x/f`, FSWrite | Net},
		{`curl -o $F https://x`, FSWrite | Net},
		{`curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8888/health`, Net},
		{`curl -sSLo /dev/null https://x`, Net},
		{`curl --output=/dev/null https://x`, Net},
		{`curl -fsSL https://x | sh`, Net | Unknown},
		{`wget https://x`, FSWrite | Net},
		{`awk '{print $1}' f`, Read},
		{`awk '{print > "f"}' f`, Unknown},
		{`awk 'BEGIN{system("rm x")}'`, Unknown},
		{`rsync -a src/ dst/`, FSWrite},
		{`rsync -a src/ host:dst/`, FSWrite | Net},
		{`/usr/bin/ls`, Read},
		{`./ls`, Unknown},
		{`/opt/x/ls`, Unknown},
		{`go build ./...`, Read},
		{`go get x`, FSWrite | Net},
		{`go env`, Read},
		{`go env -w X=1`, FSWrite},
		{`go run .`, Unknown},
		{`gofmt -l .`, Read},
		{`gofmt -w .`, FSWrite},
		{`unzip a.zip`, FSWrite},
		{`unzip -l a.zip`, Read},
		{`npm install`, FSWrite | Net},
		{`npm run build`, Unknown},
		{`npx ng build`, Unknown},
		{`docker ps`, Read},
		{`docker run x`, Unknown},
		{`kubectl get pods`, Read},
		{`kubectl apply -f x`, Net},
		{`gh pr list`, Net},
		{`rm -rf x`, FSDelete},
		{`cp a b`, FSWrite},
		{`python x.py`, Unknown},
		{`python -c 'print(1)'`, Unknown},
		{`perl -pi -e 's/a/b/' f`, FSWrite | Unknown},
		{`kill 123`, Unknown},
		{`echo hi`, Read},
		{`cd dir`, Read},
		{`git $SUB`, Unknown},
		{`ls $DIR`, Read},
		{`sudo -i`, Unknown},
	}
	tbl := Builtin()
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			a := Analyze(tt.command, tbl)
			if a.Err != nil {
				t.Fatal(a.Err)
			}
			if got := a.Class(); got != tt.want {
				t.Errorf("class = %s, want %s\n%s", got, tt.want, dump(a))
			}
		})
	}
}

func TestMerge(t *testing.T) {
	base := Table{"foo": Unknown, "bar": Read}
	merged := base.Merge(map[string]Class{"foo": Read, "baz": Net})
	if merged["foo"] != Read || merged["bar"] != Read || merged["baz"] != Net {
		t.Errorf("merged = %v", merged)
	}
	if base["foo"] != Unknown || len(base) != 2 {
		t.Errorf("receiver mutated: %v", base)
	}
	a := Analyze("foo x", merged)
	if a.Class() != Read {
		t.Errorf("override not applied: %s", dump(a))
	}
}
