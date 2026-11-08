package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Setup is a whole install's configuration keyed by name, with no row ids and
// no timestamps, so it can move between installs. Children, CustomServices and
// Buttons keep stored order; Toggles sort by (Child, Service). A service ref is
// a built-in id or "custom:<name>", where <name> is always a service name: a
// ref that looks like a row id, such as "custom:12", still names a service, so
// ParseCustomID and IsCustomServiceID must never be used on one.
type Setup struct {
	Children       []SetupChild
	CustomServices []SetupService
	Buttons        []SetupButton
	Toggles        []SetupToggle
}

// SetupChild is a child and its persistent client names.
type SetupChild struct {
	Name    string
	Clients []string
}

// SetupService is a custom service and its domains, in stored order.
type SetupService struct {
	Name    string
	Domains []string
}

// SetupButton is a button naming its child. Services and Icon hold service
// refs; Icon is "" for none.
type SetupButton struct {
	Label    string
	Child    string
	Services []string
	Icon     string
	Duration time.Duration
}

// SetupToggle is a permanent toggle naming its child, with a service ref.
type SetupToggle struct {
	Child   string
	Service string
	State   string
}

// ErrNotEmpty refuses an import into an install that already has setup. Kinds
// names every kind present, in setupKinds order.
type ErrNotEmpty struct {
	Kinds []string
}

func (e *ErrNotEmpty) Error() string {
	return "install already has " + strings.Join(e.Kinds, ", ")
}

// setupKinds lists what makes an install non-empty. Grants are not setup: they
// neither count here nor travel in the file.
var setupKinds = []struct{ name, table string }{
	{"children", "children"},
	{"buttons", "buttons"},
	{"custom services", "custom_services"},
	{"toggles", "service_toggles"},
}

// EnsureEmpty returns *ErrNotEmpty naming every kind of setup the install
// already has, or nil when it has none.
func (st *Store) EnsureEmpty(ctx context.Context) error {
	if err := ensureEmpty(ctx, st.db); err != nil {
		return wrapSetupErr("ensure empty", err)
	}
	return nil
}

func ensureEmpty(ctx context.Context, q querier) error {
	var present []string
	for _, k := range setupKinds {
		var exists bool
		if err := q.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM `+k.table+`)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			present = append(present, k.name)
		}
	}
	if len(present) > 0 {
		return &ErrNotEmpty{Kinds: present}
	}
	return nil
}

// ExportSetup reads the whole setup in one transaction, so a concurrent write
// cannot land between two of its reads. A stored ref to a custom service that
// no longer exists is an error: the file it would produce fails its own
// import. Every list is non-nil.
func (st *Store) ExportSetup(ctx context.Context) (Setup, error) {
	var out Setup
	err := st.inTx(ctx, func(tx *sql.Tx) (err error) {
		out, err = readSetup(ctx, tx)
		return err
	})
	if err != nil {
		return Setup{}, fmt.Errorf("export setup: %w", err)
	}
	return out, nil
}

// readSetup runs each query to completion before the next (single-connection
// rule) and only then maps ids to names.
func readSetup(ctx context.Context, q querier) (Setup, error) {
	children, err := loadChildren(ctx, q)
	if err != nil {
		return Setup{}, err
	}
	services, err := loadCustomServices(ctx, q, 0)
	if err != nil {
		return Setup{}, err
	}
	buttons, err := loadButtons(ctx, q)
	if err != nil {
		return Setup{}, err
	}
	toggles, err := loadSetupToggles(ctx, q)
	if err != nil {
		return Setup{}, err
	}

	out := Setup{
		Children:       make([]SetupChild, 0, len(children)),
		CustomServices: make([]SetupService, 0, len(services)),
		Buttons:        make([]SetupButton, 0, len(buttons)),
		Toggles:        make([]SetupToggle, 0, len(toggles)),
	}
	childNames := make(map[int64]string, len(children))
	for _, c := range children {
		childNames[c.ID] = c.Name
		out.Children = append(out.Children, SetupChild{Name: c.Name, Clients: c.Clients})
	}
	serviceNames := make(map[string]string, len(services))
	for _, s := range services {
		serviceNames[s.ServiceID()] = s.Name
		out.CustomServices = append(out.CustomServices, SetupService{Name: s.Name, Domains: s.Domains})
	}
	for _, b := range buttons {
		sb := SetupButton{
			Label:    b.Label,
			Child:    childNames[b.ChildID],
			Services: make([]string, 0, len(b.Services)),
			Duration: b.Duration,
		}
		for _, id := range b.Services {
			ref, err := exportRef(id, serviceNames)
			if err != nil {
				return Setup{}, fmt.Errorf("button %q: %w", b.Label, err)
			}
			sb.Services = append(sb.Services, ref)
		}
		if b.Icon != "" {
			if sb.Icon, err = exportRef(b.Icon, serviceNames); err != nil {
				return Setup{}, fmt.Errorf("button %q icon: %w", b.Label, err)
			}
		}
		out.Buttons = append(out.Buttons, sb)
	}
	for _, t := range toggles {
		ref, err := exportRef(t.ServiceID, serviceNames)
		if err != nil {
			return Setup{}, fmt.Errorf("toggle for %q: %w", childNames[t.ChildID], err)
		}
		out.Toggles = append(out.Toggles, SetupToggle{Child: childNames[t.ChildID], Service: ref, State: t.State})
	}
	// Sorted by name, not by ListServiceToggles' (child_id, service_id): both
	// ids change on import, so only names give the same order on both sides.
	sort.Slice(out.Toggles, func(i, j int) bool {
		a, b := out.Toggles[i], out.Toggles[j]
		if a.Child != b.Child {
			return a.Child < b.Child
		}
		return a.Service < b.Service
	})
	return out, nil
}

// exportRef turns a stored service id into a file ref. names maps each custom
// catalogue id to its service name.
func exportRef(id string, names map[string]string) (string, error) {
	if !strings.HasPrefix(id, customPrefix) {
		return id, nil
	}
	name, ok := names[id]
	if !ok {
		return "", fmt.Errorf("service %q does not exist", id)
	}
	return customPrefix + name, nil
}

// loadSetupToggles reads the toggles on q so an export stays inside its
// transaction; ListServiceToggles reads st.db, which would wait on the single
// connection this transaction holds.
func loadSetupToggles(ctx context.Context, q querier) ([]ServiceToggle, error) {
	rows, err := q.QueryContext(ctx, `SELECT child_id, service_id, state FROM service_toggles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceToggle{}
	for rows.Next() {
		var t ServiceToggle
		if err := rows.Scan(&t.ChildID, &t.ServiceID, &t.State); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ImportSetup writes s into an empty install in one transaction, resolving
// names to fresh row ids; any failure leaves no row behind. Emptiness is
// checked again inside the transaction, so of two racing imports one gets
// *ErrNotEmpty rather than a constraint failure. s is trusted to be valid: the
// caller validates the file, and the constraints are only the safety net.
func (st *Store) ImportSetup(ctx context.Context, s Setup) error {
	err := st.inTx(ctx, func(tx *sql.Tx) error {
		if err := ensureEmpty(ctx, tx); err != nil {
			return err
		}
		return writeSetup(ctx, tx, s)
	})
	if err != nil {
		return wrapSetupErr("import setup", err)
	}
	return nil
}

func writeSetup(ctx context.Context, tx *sql.Tx, s Setup) error {
	childIDs := make(map[string]int64, len(s.Children))
	for _, c := range s.Children {
		id, err := insertChild(ctx, tx, c.Name, normaliseClients(c.Clients))
		if err != nil {
			return err
		}
		childIDs[c.Name] = id
	}
	serviceIDs := make(map[string]string, len(s.CustomServices))
	for _, svc := range s.CustomServices {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO custom_services (name, name_key) VALUES (?, ?)`, svc.Name, nameKey(svc.Name))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := insertDomains(ctx, tx, id, svc.Domains); err != nil {
			return err
		}
		serviceIDs[customPrefix+svc.Name] = CustomServiceID(id)
	}
	for i, b := range s.Buttons {
		btn, err := resolveButton(b, childIDs, serviceIDs)
		if err != nil {
			return fmt.Errorf("button %q: %w", b.Label, err)
		}
		if err := insertButton(ctx, tx, i, btn); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	for _, t := range s.Toggles {
		childID, err := resolveChild(t.Child, childIDs)
		if err != nil {
			return fmt.Errorf("toggle: %w", err)
		}
		id, err := resolveRef(t.Service, serviceIDs)
		if err != nil {
			return fmt.Errorf("toggle for %q: %w", t.Child, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO service_toggles (child_id, service_id, state, updated_at) VALUES (?, ?, ?, ?)`,
			childID, id, t.State, now); err != nil {
			return err
		}
	}
	return nil
}

// resolveButton maps b's names to the ids they received in this import. The
// icon is resolved on its own: it need not be one of the button's services.
func resolveButton(b SetupButton, childIDs map[string]int64, serviceIDs map[string]string) (Button, error) {
	childID, err := resolveChild(b.Child, childIDs)
	if err != nil {
		return Button{}, err
	}
	out := Button{
		ChildID:  childID,
		Label:    b.Label,
		Services: make([]string, 0, len(b.Services)),
		Duration: b.Duration,
	}
	for _, ref := range b.Services {
		id, err := resolveRef(ref, serviceIDs)
		if err != nil {
			return Button{}, err
		}
		out.Services = append(out.Services, id)
	}
	if b.Icon != "" {
		if out.Icon, err = resolveRef(b.Icon, serviceIDs); err != nil {
			return Button{}, err
		}
	}
	return out, nil
}

func resolveChild(name string, childIDs map[string]int64) (int64, error) {
	id, ok := childIDs[name]
	if !ok {
		return 0, fmt.Errorf("child %q is not in the setup", name)
	}
	return id, nil
}

// resolveRef turns a file ref into a stored service id. serviceIDs maps each
// "custom:<name>" ref to its new catalogue id.
func resolveRef(ref string, serviceIDs map[string]string) (string, error) {
	if !strings.HasPrefix(ref, customPrefix) {
		return ref, nil
	}
	id, ok := serviceIDs[ref]
	if !ok {
		return "", fmt.Errorf("service %q is not in the setup", ref)
	}
	return id, nil
}

// wrapSetupErr passes *ErrNotEmpty through untouched — callers match on it —
// and wraps everything else with the operation.
func wrapSetupErr(op string, err error) error {
	var notEmpty *ErrNotEmpty
	if errors.As(err, &notEmpty) {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}
