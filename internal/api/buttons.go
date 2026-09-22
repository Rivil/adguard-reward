package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Rivil/adguard-reward/internal/grants"
	"github.com/Rivil/adguard-reward/internal/store"
)

// maxLabelRunes bounds a button label: it has to fit on a phone-width tile.
const maxLabelRunes = 64

// buttonView is the wire shape of one button; services is never null and
// duration is whole seconds.
type buttonView struct {
	ID       int64    `json:"id"`
	Label    string   `json:"label"`
	ChildID  int64    `json:"child_id"`
	Services []string `json:"services"`
	Duration int      `json:"duration"`
}

func buttonViewOf(b store.Button) buttonView {
	if b.Services == nil {
		b.Services = []string{}
	}
	return buttonView{
		ID:       b.ID,
		Label:    b.Label,
		ChildID:  b.ChildID,
		Services: b.Services,
		Duration: int(b.Duration / time.Second),
	}
}

type buttonsBody struct {
	Buttons []buttonView `json:"buttons"`
}

// buttonInput is one item of a PUT. It carries no id: the list is replaced
// whole and the server assigns ids, so a stray "id" is an unknown field.
type buttonInput struct {
	Label    string   `json:"label"`
	ChildID  int64    `json:"child_id"`
	Services []string `json:"services"`
	Duration int      `json:"duration"` // seconds
}

type buttonsRequest struct {
	Buttons *[]buttonInput `json:"buttons"`
}

// handleButtonsList is GET /api/v1/buttons: the stored list in order. It
// never touches AdGuard.
func (a *API) handleButtonsList(w http.ResponseWriter, r *http.Request) {
	rows, err := a.deps.Buttons.ListButtons(r.Context())
	if err != nil {
		a.log.Error("list buttons failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list buttons")
		return
	}
	writeButtons(w, rows)
}

func writeButtons(w http.ResponseWriter, rows []store.Button) {
	out := make([]buttonView, 0, len(rows))
	for _, b := range rows {
		out = append(out, buttonViewOf(b))
	}
	writeJSON(w, http.StatusOK, buttonsBody{out})
}

// handleButtonsReplace is PUT /api/v1/buttons: the whole list, validated
// cheapest-first and stopping at the first bad item, so a 422 names exactly
// one buttons[i].<field>. Children are checked before any AdGuard call and
// the catalogue is read once, only when there is something to check.
func (a *API) handleButtonsReplace(w http.ResponseWriter, r *http.Request) {
	var in buttonsRequest
	if !decodeGrantBody(w, r, &in, `{"buttons":[{label, child_id, services, duration}]}`) {
		return
	}
	if in.Buttons == nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "buttons is required")
		return
	}

	items := make([]store.Button, 0, len(*in.Buttons))
	for i, b := range *in.Buttons {
		label := strings.TrimSpace(b.Label)
		if n := utf8.RuneCountInString(label); n == 0 || n > maxLabelRunes {
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
				fmt.Sprintf("buttons[%d].label: must be 1-%d characters", i, maxLabelRunes))
			return
		}
		d := time.Duration(b.Duration) * time.Second
		if b.Duration <= 0 || d < grants.MinDuration || d > grants.MaxDuration {
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
				fmt.Sprintf("buttons[%d].duration: must be between %d and %d seconds", i,
					int(grants.MinDuration/time.Second), int(grants.MaxDuration/time.Second)))
			return
		}
		services := make([]string, 0, len(b.Services))
		seen := map[string]bool{}
		for _, s := range b.Services {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			services = append(services, s)
		}
		if len(services) == 0 {
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
				fmt.Sprintf("buttons[%d].services: must name at least one service", i))
			return
		}
		items = append(items, store.Button{Label: label, ChildID: b.ChildID, Services: services, Duration: d})
	}

	checked := map[int64]bool{}
	for i, b := range items {
		if checked[b.ChildID] {
			continue
		}
		if _, err := a.deps.Children.GetChild(r.Context(), b.ChildID); err != nil {
			if errors.Is(err, store.ErrChildNotFound) {
				writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
					fmt.Sprintf("buttons[%d].child_id: no child with id %d", i, b.ChildID))
				return
			}
			a.writeStoreError(w, "get child", "", err)
			return
		}
		checked[b.ChildID] = true
	}

	if len(items) > 0 {
		catalogue, err := a.deps.AdGuard.Services(r.Context())
		if err != nil {
			a.writeAdGuardError(w, "replace buttons", err)
			return
		}
		known := make(map[string]bool, len(catalogue))
		for _, s := range catalogue {
			known[s.ID] = true
		}
		for i, b := range items {
			for _, s := range b.Services {
				if !known[s] {
					writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
						fmt.Sprintf("buttons[%d].services: unknown service %q", i, s))
					return
				}
			}
		}
	}

	stored, err := a.deps.Buttons.ReplaceButtons(r.Context(), items)
	if err != nil {
		var unknown *store.ErrUnknownChild
		if errors.As(err, &unknown) {
			// The child vanished between the check above and the write; the
			// store rolled back, so the old list is intact.
			writeError(w, http.StatusUnprocessableEntity, CodeUnprocessable,
				fmt.Sprintf("buttons[%d].child_id: no child with id %d", indexOfChild(items, unknown.ChildID), unknown.ChildID))
			return
		}
		a.log.Error("replace buttons failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not replace buttons")
		return
	}
	writeButtons(w, stored)
}

func indexOfChild(items []store.Button, childID int64) int {
	for i, b := range items {
		if b.ChildID == childID {
			return i
		}
	}
	return 0
}
