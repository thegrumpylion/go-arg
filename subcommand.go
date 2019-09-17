package arg

import (
	"context"
	"reflect"
)

// Subcommand interfaces

type Runner interface {
	Run(ctx context.Context) error
}

type PreRunner interface {
	PreRun(ctx context.Context) error
}

type PersistentPreRunner interface {
	PersistentPreRun(ctx context.Context) error
}

type PostRunner interface {
	PostRun(ctx context.Context) error
}

type PersistentPostRunner interface {
	PersistentPostRun(ctx context.Context) error
}

var runnerType = reflect.TypeOf((*Runner)(nil)).Elem()
var preRunnerType = reflect.TypeOf((*PreRunner)(nil)).Elem()
var persistentPreRunnerType = reflect.TypeOf((*PersistentPreRunner)(nil)).Elem()
var postRunnerType = reflect.TypeOf((*PostRunner)(nil)).Elem()
var persistentPostRunnerType = reflect.TypeOf((*PersistentPostRunner)(nil)).Elem()

func isRunner(t reflect.Type) bool {
	return t.Implements(runnerType) || t.Implements(preRunnerType) ||
		t.Implements(persistentPreRunnerType) || t.Implements(postRunnerType) ||
		t.Implements(persistentPostRunnerType)
}

// Subcommand returns the user struct for the subcommand selected by
// the command line arguments most recently processed by the parser.
// The return value is always a pointer to a struct. If no subcommand
// was specified then it returns the top-level arguments struct. If
// no command line arguments have been processed by this parser then it
// returns nil.
func (p *Parser) Subcommand() interface{} {
	if p.lastCmd == nil || p.lastCmd.parent == nil {
		return nil
	}
	return p.val(p.lastCmd.dest).Interface()
}

// SubcommandNames returns the sequence of subcommands specified by the
// user. If no subcommands were given then it returns an empty slice.
func (p *Parser) SubcommandNames() []string {
	if p.lastCmd == nil {
		return nil
	}

	// make a list of ancestor commands
	var ancestors []string
	cur := p.lastCmd
	for cur.parent != nil { // we want to exclude the root
		ancestors = append(ancestors, cur.name)
		cur = cur.parent
	}

	// reverse the list
	out := make([]string, len(ancestors))
	for i := 0; i < len(ancestors); i++ {
		out[i] = ancestors[len(ancestors)-i-1]
	}
	return out
}
