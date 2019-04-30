package arg

import (
	"encoding"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	scalar "github.com/alexflint/go-scalar"
)

// path represents a sequence of steps to find the output location for an
// argument or subcommand in the final destination struct
type path struct {
	root   int      // index of the destination struct
	fields []string // sequence of struct field names to traverse
}

// String gets a string representation of the given path
func (p path) String() string {
	return "args." + strings.Join(p.fields, ".")
}

// Child gets a new path representing a child of this path.
func (p path) Child(child string) path {
	// copy the entire slice of fields to avoid possible slice overwrite
	subfields := make([]string, len(p.fields)+1)
	copy(subfields, append(p.fields, child))
	return path{
		root:   p.root,
		fields: subfields,
	}
}

// spec represents a command line option
type spec struct {
	dest       path
	typ        reflect.Type
	long       string
	short      string
	multiple   bool
	required   bool
	positional bool
	separate   bool
	help       string
	env        string
	boolean    bool
}

// command represents a named subcommand, or the top-level command
type command struct {
	name        string
	dest        path
	specs       []*spec
	subcommands []*command
}

// ErrHelp indicates that -h or --help were provided
var ErrHelp = errors.New("help requested by user")

// ErrVersion indicates that --version was provided
var ErrVersion = errors.New("version requested by user")

// MustParse processes command line arguments and exits upon failure
func MustParse(dest ...interface{}) *Parser {
	p, err := NewParser(Config{}, dest...)
	if err != nil {
		fmt.Println(err)
		os.Exit(-1)
	}
	err = p.Parse(flags())
	if err == ErrHelp {
		p.WriteHelp(os.Stdout)
		os.Exit(0)
	}
	if err == ErrVersion {
		fmt.Println(p.version)
		os.Exit(0)
	}
	if err != nil {
		p.Fail(err.Error())
	}
	return p
}

// Parse processes command line arguments and stores them in dest
func Parse(dest ...interface{}) error {
	p, err := NewParser(Config{}, dest...)
	if err != nil {
		return err
	}
	return p.Parse(flags())
}

// flags gets all command line arguments other than the first (program name)
func flags() []string {
	if len(os.Args) == 0 { // os.Args could be empty
		return nil
	}
	return os.Args[1:]
}

// Config represents configuration options for an argument parser
type Config struct {
	Program string // Program is the name of the program used in the help text
}

// Parser represents a set of command line options with destination values
type Parser struct {
	cmd         *command
	roots       []reflect.Value
	config      Config
	version     string
	description string
}

// Versioned is the interface that the destination struct should implement to
// make a version string appear at the top of the help message.
type Versioned interface {
	// Version returns the version string that will be printed on a line by itself
	// at the top of the help message.
	Version() string
}

// Described is the interface that the destination struct should implement to
// make a description string appear at the top of the help message.
type Described interface {
	// Description returns the string that will be printed on a line by itself
	// at the top of the help message.
	Description() string
}

// walkFields calls a function for each field of a struct, recursively expanding struct fields.
func walkFields(t reflect.Type, visit func(field reflect.StructField, owner reflect.Type) bool) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		expand := visit(field, t)
		if expand && field.Type.Kind() == reflect.Struct {
			walkFields(field.Type, visit)
		}
	}
}

// NewParser constructs a parser from a list of destination structs
func NewParser(config Config, dests ...interface{}) (*Parser, error) {
	// first pick a name for the command for use in the usage text
	var name string
	switch {
	case config.Program != "":
		name = config.Program
	case len(os.Args) > 0:
		name = filepath.Base(os.Args[0])
	default:
		name = "program"
	}

	// construct a parser
	p := Parser{
		cmd:    &command{name: name},
		config: config,
	}

	// make a list of roots
	for _, dest := range dests {
		p.roots = append(p.roots, reflect.ValueOf(dest))
	}

	// process each of the destination values
	for i, dest := range dests {
		t := reflect.TypeOf(dest)
		if t.Kind() != reflect.Ptr {
			panic(fmt.Sprintf("%s is not a pointer (did you forget an ampersand?)", t))
		}

		cmd, err := cmdFromStruct(name, path{root: i}, t)
		if err != nil {
			return nil, err
		}
		p.cmd.specs = append(p.cmd.specs, cmd.specs...)
		p.cmd.subcommands = append(p.cmd.subcommands, cmd.subcommands...)

		if dest, ok := dest.(Versioned); ok {
			p.version = dest.Version()
		}
		if dest, ok := dest.(Described); ok {
			p.description = dest.Description()
		}
	}

	return &p, nil
}

func cmdFromStruct(name string, dest path, t reflect.Type) (*command, error) {
	// commands can only be created from pointers to structs
	if t.Kind() != reflect.Ptr {
		return nil, fmt.Errorf("subcommands must be pointers to structs but %s is a %s",
			dest, t.Kind())
	}

	t = t.Elem()
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("subcommands must be pointers to structs but %s is a pointer to %s",
			dest, t.Kind())
	}

	cmd := command{
		name: name,
		dest: dest,
	}

	var errs []string
	walkFields(t, func(field reflect.StructField, t reflect.Type) bool {
		// Check for the ignore switch in the tag
		tag := field.Tag.Get("arg")
		if tag == "-" {
			return false
		}

		// If this is an embedded struct then recurse into its fields
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			return true
		}

		// duplicate the entire path to avoid slice overwrites
		subdest := dest.Child(field.Name)
		spec := spec{
			dest: subdest,
			long: strings.ToLower(field.Name),
			typ:  field.Type,
		}

		help, exists := field.Tag.Lookup("help")
		if exists {
			spec.help = help
		}

		// Look at the tag
		var isSubcommand bool // tracks whether this field is a subcommand
		if tag != "" {
			for _, key := range strings.Split(tag, ",") {
				key = strings.TrimLeft(key, " ")
				var value string
				if pos := strings.Index(key, ":"); pos != -1 {
					value = key[pos+1:]
					key = key[:pos]
				}

				switch {
				case strings.HasPrefix(key, "---"):
					errs = append(errs, fmt.Sprintf("%s.%s: too many hyphens", t.Name(), field.Name))
				case strings.HasPrefix(key, "--"):
					spec.long = key[2:]
				case strings.HasPrefix(key, "-"):
					if len(key) != 2 {
						errs = append(errs, fmt.Sprintf("%s.%s: short arguments must be one character only",
							t.Name(), field.Name))
						return false
					}
					spec.short = key[1:]
				case key == "required":
					spec.required = true
				case key == "positional":
					spec.positional = true
				case key == "separate":
					spec.separate = true
				case key == "help": // deprecated
					spec.help = value
				case key == "env":
					// Use override name if provided
					if value != "" {
						spec.env = value
					} else {
						spec.env = strings.ToUpper(field.Name)
					}
				case key == "subcommand":
					// decide on a name for the subcommand
					cmdname := value
					if cmdname == "" {
						cmdname = strings.ToLower(field.Name)
					}

					// parse the subcommand recursively
					subcmd, err := cmdFromStruct(cmdname, subdest, field.Type)
					if err != nil {
						errs = append(errs, err.Error())
						return false
					}

					cmd.subcommands = append(cmd.subcommands, subcmd)
					isSubcommand = true
					fmt.Println("found a subcommand")
				default:
					errs = append(errs, fmt.Sprintf("unrecognized tag '%s' on field %s", key, tag))
					return false
				}
			}
		}

		// Check whether this field is supported. It's good to do this here rather than
		// wait until ParseValue because it means that a program with invalid argument
		// fields will always fail regardless of whether the arguments it received
		// exercised those fields.
		if !isSubcommand {
			cmd.specs = append(cmd.specs, &spec)

			var parseable bool
			parseable, spec.boolean, spec.multiple = canParse(field.Type)
			if !parseable {
				errs = append(errs, fmt.Sprintf("%s.%s: %s fields are not supported",
					t.Name(), field.Name, field.Type.String()))
				return false
			}
		}

		// if this was an embedded field then we already returned true up above
		return false
	})

	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "\n"))
	}

	// check that we don't have both positionals and subcommands
	var hasPositional bool
	for _, spec := range cmd.specs {
		if spec.positional {
			hasPositional = true
		}
	}
	if hasPositional && len(cmd.subcommands) > 0 {
		return nil, fmt.Errorf("%T cannot have both subcommands and positional arguments", t)
	}

	fmt.Printf("parsed a command with %d subcommands\n", len(cmd.subcommands))

	return &cmd, nil
}

// Parse processes the given command line option, storing the results in the field
// of the structs from which NewParser was constructed
func (p *Parser) Parse(args []string) error {
	// If -h or --help were specified then print usage
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return ErrHelp
		}
		if arg == "--version" {
			return ErrVersion
		}
		if arg == "--" {
			break
		}
	}

	// Process all command line arguments
	return p.process(args)
}

// process environment vars for the given arguments
func (p *Parser) captureEnvVars(specs []*spec, wasPresent map[*spec]bool) error {
	for _, spec := range specs {
		if spec.env == "" {
			continue
		}

		value, found := os.LookupEnv(spec.env)
		if !found {
			continue
		}

		if spec.multiple {
			// expect a CSV string in an environment
			// variable in the case of multiple values
			values, err := csv.NewReader(strings.NewReader(value)).Read()
			if err != nil {
				return fmt.Errorf(
					"error reading a CSV string from environment variable %s with multiple values: %v",
					spec.env,
					err,
				)
			}
			if err = setSlice(p.writable(spec.dest), values, !spec.separate); err != nil {
				return fmt.Errorf(
					"error processing environment variable %s with multiple values: %v",
					spec.env,
					err,
				)
			}
		} else {
			if err := scalar.ParseValue(p.writable(spec.dest), value); err != nil {
				return fmt.Errorf("error processing environment variable %s: %v", spec.env, err)
			}
		}
		wasPresent[spec] = true
	}

	return nil
}

// process goes through arguments one-by-one, parses them, and assigns the result to
// the underlying struct field
func (p *Parser) process(args []string) error {
	// track the options we have seen
	wasPresent := make(map[*spec]bool)

	// union of specs for the chain of subcommands encountered so far
	curCmd := p.cmd

	// make a copy of the specs because we will add to this list each time we expand a subcommand
	specs := make([]*spec, len(curCmd.specs))
	copy(specs, curCmd.specs)

	// deal with environment vars
	err := p.captureEnvVars(specs, wasPresent)
	if err != nil {
		return err
	}

	// process each string from the command line
	var allpositional bool
	var positionals []string

	// must use explicit for loop, not range, because we manipulate i inside the loop
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			allpositional = true
			continue
		}

		if !isFlag(arg) || allpositional {
			// each subcommand can have either subcommands or positionals, but not both
			fmt.Printf("processing %q, with %d subcommands", arg, len(curCmd.subcommands))
			if len(curCmd.subcommands) == 0 {
				positionals = append(positionals, arg)
				continue
			}

			// if we have a subcommand then make sure it is valid for the current context
			subcmd := findSubcommand(curCmd.subcommands, arg)
			if subcmd == nil {
				return fmt.Errorf("invalid subcommand: %s", arg)
			}

			// instantiate the field to point to a new struct
			v := p.writable(subcmd.dest)
			v.Set(reflect.New(v.Type().Elem())) // we already checked that all subcommands are struct pointers

			// add the new options to the set of allowed options
			specs = append(specs, subcmd.specs...)

			// capture environment vars for these new options
			err := p.captureEnvVars(subcmd.specs, wasPresent)
			if err != nil {
				return err
			}

			curCmd = subcmd
			continue
		}

		// check for an equals sign, as in "--foo=bar"
		var value string
		opt := strings.TrimLeft(arg, "-")
		if pos := strings.Index(opt, "="); pos != -1 {
			value = opt[pos+1:]
			opt = opt[:pos]
		}

		// lookup the spec for this option (note that the "specs" slice changes as
		// we expand subcommands so it is better not to use a map)
		spec := findOption(specs, opt)
		if spec == nil {
			return fmt.Errorf("unknown argument %s", arg)
		}
		wasPresent[spec] = true

		// deal with the case of multiple values
		if spec.multiple {
			var values []string
			if value == "" {
				for i+1 < len(args) && !isFlag(args[i+1]) {
					values = append(values, args[i+1])
					i++
					if spec.separate {
						break
					}
				}
			} else {
				values = append(values, value)
			}
			err := setSlice(p.writable(spec.dest), values, !spec.separate)
			if err != nil {
				return fmt.Errorf("error processing %s: %v", arg, err)
			}
			continue
		}

		// if it's a flag and it has no value then set the value to true
		// use boolean because this takes account of TextUnmarshaler
		if spec.boolean && value == "" {
			value = "true"
		}

		// if we have something like "--foo" then the value is the next argument
		if value == "" {
			if i+1 == len(args) {
				return fmt.Errorf("missing value for %s", arg)
			}
			if !nextIsNumeric(spec.typ, args[i+1]) && isFlag(args[i+1]) {
				return fmt.Errorf("missing value for %s", arg)
			}
			value = args[i+1]
			i++
		}

		err := scalar.ParseValue(p.writable(spec.dest), value)
		if err != nil {
			return fmt.Errorf("error processing %s: %v", arg, err)
		}
	}

	// process positionals
	for _, spec := range specs {
		if !spec.positional {
			continue
		}
		if len(positionals) == 0 {
			break
		}
		wasPresent[spec] = true
		if spec.multiple {
			err := setSlice(p.writable(spec.dest), positionals, true)
			if err != nil {
				return fmt.Errorf("error processing %s: %v", spec.long, err)
			}
			positionals = nil
		} else {
			err := scalar.ParseValue(p.writable(spec.dest), positionals[0])
			if err != nil {
				return fmt.Errorf("error processing %s: %v", spec.long, err)
			}
			positionals = positionals[1:]
		}
	}
	if len(positionals) > 0 {
		return fmt.Errorf("too many positional arguments at '%s'", positionals[0])
	}

	// finally check that all the required args were provided
	for _, spec := range specs {
		if spec.required && !wasPresent[spec] {
			name := spec.long
			if !spec.positional {
				name = "--" + spec.long
			}
			return fmt.Errorf("%s is required", name)
		}
	}

	return nil
}

func nextIsNumeric(t reflect.Type, s string) bool {
	switch t.Kind() {
	case reflect.Ptr:
		return nextIsNumeric(t.Elem(), s)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v := reflect.New(t)
		err := scalar.ParseValue(v, s)
		return err == nil
	default:
		return false
	}
}

// isFlag returns true if a token is a flag such as "-v" or "--user" but not "-" or "--"
func isFlag(s string) bool {
	return strings.HasPrefix(s, "-") && strings.TrimLeft(s, "-") != ""
}

// readable returns a reflect.Value corresponding to the current value for the
// given
func (p *Parser) readable(dest path) reflect.Value {
	v := p.roots[dest.root]
	for _, field := range dest.fields {
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}
			}
			v = v.Elem()
		}

		v = v.FieldByName(field)
		if !v.IsValid() {
			// it is appropriate to panic here because this can only happen due to
			// an internal bug in this library (since we construct the path ourselves
			// by reflecting on the same struct)
			panic(fmt.Errorf("error resolving path %v: %v has no field named %v",
				dest.fields, v.Type(), field))
		}
	}
	return v
}

// writable trav.patherses the destination struct to find the destination to
// which the value of the given spec should be written. It fills in null
// structs with pointers to the zero value for that struct.
func (p *Parser) writable(dest path) reflect.Value {
	v := p.roots[dest.root]
	for _, field := range dest.fields {
		if v.Kind() == reflect.Ptr {
			if v.IsNil() {
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}

		v = v.FieldByName(field)
		if !v.IsValid() {
			// it is appropriate to panic here because this can only happen due to
			// an internal bug in this library (since we construct the path ourselves
			// by reflecting on the same struct)
			panic(fmt.Errorf("error resolving path %v: %v has no field named %v",
				dest.fields, v.Type(), field))
		}
	}
	return v
}

// parse a value as the appropriate type and store it in the struct
func setSlice(dest reflect.Value, values []string, trunc bool) error {
	if !dest.CanSet() {
		return fmt.Errorf("field is not writable")
	}

	var ptr bool
	elem := dest.Type().Elem()
	if elem.Kind() == reflect.Ptr && !elem.Implements(textUnmarshalerType) {
		ptr = true
		elem = elem.Elem()
	}

	// Truncate the dest slice in case default values exist
	if trunc && !dest.IsNil() {
		dest.SetLen(0)
	}

	for _, s := range values {
		v := reflect.New(elem)
		if err := scalar.ParseValue(v.Elem(), s); err != nil {
			return err
		}
		if !ptr {
			v = v.Elem()
		}
		dest.Set(reflect.Append(dest, v))
	}
	return nil
}

// canParse returns true if the type can be parsed from a string
func canParse(t reflect.Type) (parseable, boolean, multiple bool) {
	parseable = scalar.CanParse(t)
	boolean = isBoolean(t)
	if parseable {
		return
	}

	// Look inside pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// Look inside slice types
	if t.Kind() == reflect.Slice {
		multiple = true
		t = t.Elem()
	}

	parseable = scalar.CanParse(t)
	boolean = isBoolean(t)
	if parseable {
		return
	}

	// Look inside pointer types (again, in case of []*Type)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	parseable = scalar.CanParse(t)
	boolean = isBoolean(t)
	if parseable {
		return
	}

	return false, false, false
}

var textUnmarshalerType = reflect.TypeOf([]encoding.TextUnmarshaler{}).Elem()

// isBoolean returns true if the type can be parsed from a single string
func isBoolean(t reflect.Type) bool {
	switch {
	case t.Implements(textUnmarshalerType):
		return false
	case t.Kind() == reflect.Bool:
		return true
	case t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Bool:
		return true
	default:
		return false
	}
}

// findOption finds an option from its name, or returns null if no spec is found
func findOption(specs []*spec, name string) *spec {
	for _, spec := range specs {
		if spec.positional {
			continue
		}
		if spec.long == name || spec.short == name {
			return spec
		}
	}
	return nil
}

// findSubcommand finds a subcommand using its name, or returns null if no subcommand is found
func findSubcommand(cmds []*command, name string) *command {
	for _, cmd := range cmds {
		if cmd.name == name {
			return cmd
		}
	}
	return nil
}
