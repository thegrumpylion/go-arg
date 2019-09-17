package arg

import (
	"context"
	"reflect"
)

type ctxKey struct {
	name string
}

func (key ctxKey) String() string {
	return key.name
}

type PostRunErrorsMap map[string]error

var (
	RunErrorKey      ctxKey = ctxKey{"RunErrorKey"}
	LastErrorKey     ctxKey = ctxKey{"LastErrorKey"}
	PostRunErrorsKey ctxKey = ctxKey{"PostRunnerErrorsKey"}
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

type RunnerWithID interface {
	RunnerID() string
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

type ExecutionStrategy int

const (
	NormalStrategy ExecutionStrategy = iota
	PostRunOnErrorStrategy
	ForsePostRunOnStrategy
)

func (p *Parser) Execute(ctx context.Context, strategy ExecutionStrategy) error {

	var err error
	lastCmd := len(p.lastTree) - 1
	pPostRunners := []PersistentPostRunner{}

	for i, inf := range p.lastTree {
		// PersistentPostRun pushed on a stack to run in a reverse order
		// check
		if rnr, ok := inf.(PersistentPostRunner); ok {
			pPostRunners = append([]PersistentPostRunner{rnr}, pPostRunners...)
		}
		// PersistentPreRun
		if rnr, ok := inf.(PersistentPreRunner); ok {
			if err = rnr.PersistentPreRun(ctx); err != nil {
				break
			}
		}
		if i == lastCmd {
			// PreRun
			if rnr, ok := inf.(PreRunner); ok {
				if err = rnr.PreRun(ctx); err != nil {
					break
				}
			}
			// Run
			if rnr, ok := inf.(Runner); ok {
				if err = rnr.Run(ctx); err != nil {
					break
				}
			}
			// PostRun
			if rnr, ok := inf.(PostRunner); ok {
				if err = rnr.PostRun(ctx); err != nil {
					break
				}
			}
		}
	}
	// force persistent post run regardless of error
	if strategy == ForsePostRunOnStrategy && len(pPostRunners) != 0 {
		// separate run error since it can be nil
		ctx = context.WithValue(ctx, RunErrorKey, err)
		ctx = context.WithValue(ctx, LastErrorKey, err)
		for _, rnr := range pPostRunners {
			if err = rnr.PersistentPostRun(ctx); err != nil {
				withID, ok := rnr.(RunnerWithID)
				if !ok {
					ctx = context.WithValue(ctx, LastErrorKey, err)
					continue
				}
				errInf := ctx.Value(PostRunErrorsKey)
				if errInf == nil {
					errInf = PostRunErrorsMap{}
				}
				if errMap, ok := errInf.(PostRunErrorsMap); ok {
					errMap[withID.RunnerID()] = err
					ctx = context.WithValue(ctx, PostRunErrorsKey, errMap)
					ctx = context.WithValue(ctx, LastErrorKey, err)
				}
			}
		}
		return err
	}
	// check for error and strategy
	if err != nil && strategy != PostRunOnErrorStrategy {
		return err
	}
	// PersistentPostRun
	for _, rnr := range pPostRunners {
		if err = rnr.PersistentPostRun(ctx); err != nil {
			return err
		}
	}
	return nil
}
