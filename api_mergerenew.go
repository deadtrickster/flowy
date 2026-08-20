package main

// POST /api/merge/{id}/renew - the run that holds this target is still alive.
//
// 01M0EBXHQ3. The landing lock is believed for MergeLockBelievedFor and a gate
// takes about five minutes, so the margin is ten. Nothing renewed it in between:
// the only renew was at VERDICT time, after the measurement rather than during
// it, and that one exists because a twenty-minute declare-to-verdict on
// 2026-08-18 lost its window mid-run.
//
// Five minutes fits. What does not fit is the tail - a retry, a slow box, a
// suite that keeps growing - and the tail is expensive in a way the common case
// is not: since recording a verdict now REFUSES when there is nothing to renew,
// a run that crosses the window loses the VERDICT, not just the land. Five
// minutes of measurement, discarded, with nothing written down.
//
// RENEW, NEVER TAKE. store.RenewMergeLock is an UPDATE matching holder and item,
// so it cannot acquire a target that is free, expired or somebody else's -
// renewing something you do not hold is taking it, and taking is what declare is
// for. false here means the window had already gone, which the caller needs to
// hear as "stop, your run is no longer the one holding this" rather than as a
// reason to retry.
//
// IT IS THE DRAINER'S DOOR AND IT IS DELIBERATELY DUMB. It does not check that a
// gate is running, because it cannot: the run is a process on somebody else's
// box. What bounds it is the store's own rule - only the holder can renew - and
// the caller's own lifetime, which is why the drainer ties its heartbeat to the
// gate's pid rather than to a timer.

import (
	"errors"
	"net/http"

	"github.com/deadtrickster/flowy/internal/store"
)

func (s *server) handleMergeRenew(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r)

	art, err := s.db.ReadArtifact(r.Context(), p, r.PathValue("id"), false)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody("no such merge request"))
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	held, err := s.db.RenewMergeLock(r.Context(), p, store.ProjectOfRow(art), store.TargetOf(art), art.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if !held {
		// 409 AND WHO HOLDS IT. A bare "no" leaves the caller unable to tell a
		// window that lapsed from a target somebody else took, and those want
		// different responses - stop and re-declare, or stop entirely.
		lock, lockErr := s.db.MergeLockOf(r.Context(), store.ProjectOfRow(art), store.TargetOf(art))
		if lockErr != nil {
			serverError(w, r, lockErr)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "your window on " + store.TargetOf(art) + " has gone - this renews a lock, it does not take one",
			"lock":  lock,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": art})
}
