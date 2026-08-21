package main

// The bootstrap operator becoming a fact in the store.
//
// $FLOWY_OPERATOR names who runs a node whose store holds no operator yet -
// see isOperator in auth.go, which falls through to it. That fallback answers
// the AUTH question and nothing else, and everything downstream reads the
// COLUMN: a mention of @operator resolves through users.role, and so does the
// role the roster draws. So on this node the person every seat calls the
// operator had no role row, and a feature written against the store found
// nobody - which is the empty-versus-absent failure with the two halves in
// different files.
//
// So the bootstrap converges, once, at boot: if the store holds NO operator and
// the flag names a user who exists, that user's role is written. After that the
// column is the only answer anybody needs and $FLOWY_OPERATOR is what it always
// claimed to be - the way the FIRST operator exists at all.
//
// IT NEVER OVERRIDES A STORE THAT HAS ONE. A node whose operator was changed by
// SetRole must not have a stale environment variable put the old one back on
// the next restart, which would be a privilege grant by config file - so a
// single existing operator is left exactly as it is, and the flag is ignored.
//
// AND IT IS NOT AN ERROR TO FAIL. A node that cannot write a role still serves;
// it logs what it could not do, and the operator can set it through the door
// that already exists. Refusing to start would turn a naming convenience into
// an outage.

import (
	"context"
	"log"

	"github.com/deadtrickster/flowy/internal/store"
)

func adoptBootstrapOperator(ctx context.Context, db *store.DB, operator string) {
	if operator == "" {
		return
	}
	held, err := db.UsersWithRole(ctx, store.RoleOperator)
	if err != nil {
		log.Printf("operator: cannot read who holds the role: %v", err)
		return
	}
	if len(held) > 0 {
		return
	}
	if err := db.SetRole(ctx, nil, operator, store.RoleOperator); err != nil {
		log.Printf("operator: %s is this node's bootstrap operator and the role could not be "+
			"written (%v); @operator will resolve to nobody until it is set", operator, err)
		return
	}
	log.Printf("operator: %s adopted from FLOWY_OPERATOR into the store, "+
		"so @operator names them and the roster says so", operator)
}
