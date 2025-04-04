// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package executor

import (
	"context"
	"strings"

	"github.com/pingcap/errors"
	"github.com/pingcap/tidb/pkg/domain"
	"github.com/pingcap/tidb/pkg/executor/internal/exec"
	"github.com/pingcap/tidb/pkg/infoschema"
	"github.com/pingcap/tidb/pkg/kv"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/mysql"
	"github.com/pingcap/tidb/pkg/parser/terror"
	"github.com/pingcap/tidb/pkg/privilege"
	"github.com/pingcap/tidb/pkg/sessionctx"
	"github.com/pingcap/tidb/pkg/sessiontxn"
	"github.com/pingcap/tidb/pkg/table"
	"github.com/pingcap/tidb/pkg/util/chunk"
	"github.com/pingcap/tidb/pkg/util/dbterror/exeerrors"
	"github.com/pingcap/tidb/pkg/util/logutil"
	"github.com/pingcap/tidb/pkg/util/sqlescape"
	"go.uber.org/zap"
)

/***
 * Revoke Statement
 * See https://dev.mysql.com/doc/refman/5.7/en/revoke.html
 ************************************************************************************/
var (
	_ exec.Executor = (*RevokeExec)(nil)
)

// RevokeExec executes RevokeStmt.
type RevokeExec struct {
	exec.BaseExecutor

	Privs      []*ast.PrivElem
	ObjectType ast.ObjectTypeType
	Level      *ast.GrantLevel
	Users      []*ast.UserSpec

	ctx  sessionctx.Context
	is   infoschema.InfoSchema
	done bool
}

// Next implements the Executor Next interface.
func (e *RevokeExec) Next(ctx context.Context, _ *chunk.Chunk) error {
	if e.done {
		return nil
	}
	e.done = true
	internalCtx := kv.WithInternalSourceType(context.Background(), kv.InternalTxnPrivilege)

	// Commit the old transaction, like DDL.
	if err := sessiontxn.NewTxnInStmt(ctx, e.Ctx()); err != nil {
		return err
	}
	defer func() { e.Ctx().GetSessionVars().SetInTxn(false) }()

	// Create internal session to start internal transaction.
	isCommit := false
	internalSession, err := e.GetSysSession()
	if err != nil {
		return err
	}
	defer func() {
		if !isCommit {
			_, err := internalSession.GetSQLExecutor().ExecuteInternal(internalCtx, "rollback")
			if err != nil {
				logutil.BgLogger().Error("rollback error occur at grant privilege", zap.Error(err))
			}
		}
		e.ReleaseSysSession(internalCtx, internalSession)
	}()

	_, err = internalSession.GetSQLExecutor().ExecuteInternal(internalCtx, "begin")
	if err != nil {
		return err
	}

	sessVars := e.Ctx().GetSessionVars()
	// Revoke for each user.
	for _, user := range e.Users {
		if user.User.CurrentUser {
			user.User.Username = sessVars.User.AuthUsername
			user.User.Hostname = sessVars.User.AuthHostname
		}

		// Check if user exists.
		exists, err := userExists(ctx, e.Ctx(), user.User.Username, user.User.Hostname)
		if err != nil {
			return err
		}
		if !exists {
			return errors.Errorf("Unknown user: %s", user.User)
		}
		err = e.checkDynamicPrivilegeUsage()
		if err != nil {
			return err
		}
		err = e.revokeOneUser(ctx, internalSession, user.User.Username, user.User.Hostname)
		if err != nil {
			return err
		}
	}

	_, err = internalSession.GetSQLExecutor().ExecuteInternal(internalCtx, "commit")
	if err != nil {
		return err
	}
	isCommit = true
	users := userSpecToUserList(e.Users)
	return domain.GetDomain(e.Ctx()).NotifyUpdatePrivilege(users)
}

func userSpecToUserList(specs []*ast.UserSpec) []string {
	users := make([]string, 0, len(specs))
	for _, user := range specs {
		users = append(users, user.User.Username)
	}
	return users
}

// Checks that dynamic privileges are only of global scope.
// Returns the mysql-correct error when not the case.
func (e *RevokeExec) checkDynamicPrivilegeUsage() error {
	var dynamicPrivs []string
	for _, priv := range e.Privs {
		if priv.Priv == mysql.ExtendedPriv {
			dynamicPrivs = append(dynamicPrivs, strings.ToUpper(priv.Name))
		}
	}
	if len(dynamicPrivs) > 0 && e.Level.Level != ast.GrantLevelGlobal {
		return exeerrors.ErrIllegalPrivilegeLevel.GenWithStackByArgs(strings.Join(dynamicPrivs, ","))
	}
	return nil
}

func (e *RevokeExec) revokeOneUser(ctx context.Context, internalSession sessionctx.Context, user, host string) error {
	dbName := e.Level.DBName
	if len(dbName) == 0 {
		dbName = e.Ctx().GetSessionVars().CurrentDB
	}

	// If there is no privilege entry in corresponding table, insert a new one.
	// DB scope:		mysql.DB
	// Table scope:		mysql.Tables_priv
	// Column scope:	mysql.Columns_priv
	switch e.Level.Level {
	case ast.GrantLevelDB:
		ok, err := dbUserExists(internalSession, user, host, dbName)
		if err != nil {
			return err
		}
		if !ok {
			return errors.Errorf("There is no such grant defined for user '%s' on host '%s' on database %s", user, host, dbName)
		}
	case ast.GrantLevelTable:
		ok, err := tableUserExists(internalSession, user, host, dbName, e.Level.TableName)
		if err != nil {
			return err
		}
		if !ok {
			return errors.Errorf("There is no such grant defined for user '%s' on host '%s' on table %s.%s", user, host, dbName, e.Level.TableName)
		}
	}

	for _, priv := range e.Privs {
		err := e.revokePriv(ctx, internalSession, priv, user, host)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *RevokeExec) revokePriv(ctx context.Context, internalSession sessionctx.Context, priv *ast.PrivElem, user, host string) error {
	if priv.Priv == mysql.UsagePriv {
		return nil
	}
	switch e.Level.Level {
	case ast.GrantLevelGlobal:
		return e.revokeGlobalPriv(internalSession, priv, user, host)
	case ast.GrantLevelDB:
		return e.revokeDBPriv(internalSession, priv, user, host)
	case ast.GrantLevelTable:
		if len(priv.Cols) == 0 {
			return e.revokeTablePriv(ctx, internalSession, priv, user, host)
		}
		return e.revokeColumnPriv(ctx, internalSession, priv, user, host)
	}
	return errors.Errorf("Unknown revoke level: %#v", e.Level)
}

func (e *RevokeExec) revokeDynamicPriv(internalSession sessionctx.Context, privName string, user, host string) error {
	privName = strings.ToUpper(privName)
	if !privilege.GetPrivilegeManager(e.Ctx()).IsDynamicPrivilege(privName) { // for MySQL compatibility
		e.Ctx().GetSessionVars().StmtCtx.AppendWarning(exeerrors.ErrDynamicPrivilegeNotRegistered.FastGenByArgs(privName))
	}
	ctx := kv.WithInternalSourceType(context.Background(), kv.InternalTxnPrivilege)
	_, err := internalSession.GetSQLExecutor().ExecuteInternal(ctx, "DELETE FROM mysql.global_grants WHERE user = %? AND host = %? AND priv = %?", user, host, privName)
	return err
}

func (e *RevokeExec) revokeGlobalPriv(internalSession sessionctx.Context, priv *ast.PrivElem, user, host string) error {
	ctx := kv.WithInternalSourceType(context.Background(), kv.InternalTxnPrivilege)
	if priv.Priv == mysql.ExtendedPriv {
		return e.revokeDynamicPriv(internalSession, priv.Name, user, host)
	}
	if priv.Priv == mysql.AllPriv { // If ALL, also revoke dynamic privileges
		_, err := internalSession.GetSQLExecutor().ExecuteInternal(ctx, "DELETE FROM mysql.global_grants WHERE user = %? AND host = %?", user, host)
		if err != nil {
			return err
		}
	}
	sql := new(strings.Builder)
	sqlescape.MustFormatSQL(sql, "UPDATE %n.%n SET ", mysql.SystemDB, mysql.UserTable)
	err := composeGlobalPrivUpdate(sql, priv.Priv, "N")
	if err != nil {
		return err
	}
	sqlescape.MustFormatSQL(sql, " WHERE User=%? AND Host=%?", user, strings.ToLower(host))

	_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
	return err
}

func (e *RevokeExec) revokeDBPriv(internalSession sessionctx.Context, priv *ast.PrivElem, userName, host string) error {
	ctx := kv.WithInternalSourceType(context.Background(), kv.InternalTxnPrivilege)
	dbName := e.Level.DBName
	if len(dbName) == 0 {
		dbName = e.Ctx().GetSessionVars().CurrentDB
	}

	sql := new(strings.Builder)
	sqlescape.MustFormatSQL(sql, "UPDATE %n.%n SET ", mysql.SystemDB, mysql.DBTable)
	err := composeDBPrivUpdate(sql, priv.Priv, "N")
	if err != nil {
		return err
	}
	sqlescape.MustFormatSQL(sql, " WHERE User=%? AND Host=%? AND DB=%?", userName, host, dbName)

	_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
	if err != nil {
		return err
	}

	sql = new(strings.Builder)
	sqlescape.MustFormatSQL(sql, "DELETE FROM %n.%n WHERE User=%? AND Host=%? AND DB=%?", mysql.SystemDB, mysql.DBTable, userName, host, dbName)

	for _, v := range append(mysql.AllDBPrivs, mysql.GrantPriv) {
		sqlescape.MustFormatSQL(sql, " AND %n='N'", v.ColumnString())
	}
	_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
	return err
}

func (e *RevokeExec) revokeTablePriv(ctx context.Context, internalSession sessionctx.Context, priv *ast.PrivElem, user, host string) error {
	ctx = kv.WithInternalSourceType(ctx, kv.InternalTxnPrivilege)
	dbName, tbl, err := getTargetSchemaAndTable(ctx, e.Ctx(), e.Level.DBName, e.Level.TableName, e.is)
	if err != nil && !terror.ErrorEqual(err, infoschema.ErrTableNotExists) {
		return err
	}

	// Allow REVOKE on non-existent table, see issue #28533
	tblName := e.Level.TableName
	if tbl != nil {
		tblName = tbl.Meta().Name.O
	}
	sql := new(strings.Builder)
	sqlescape.MustFormatSQL(sql, "UPDATE %n.%n SET ", mysql.SystemDB, mysql.TablePrivTable)
	isDelRow, err := composeTablePrivUpdateForRevoke(internalSession, sql, priv.Priv, user, host, dbName, tblName)
	if err != nil {
		return err
	}

	sqlescape.MustFormatSQL(sql, " WHERE User=%? AND Host=%? AND DB=%? AND Table_name=%?", user, host, dbName, tblName)
	_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
	if err != nil {
		return err
	}

	if isDelRow {
		sql.Reset()
		sqlescape.MustFormatSQL(sql, "DELETE FROM %n.%n WHERE User=%? AND Host=%? AND DB=%? AND Table_name=%?", mysql.SystemDB, mysql.TablePrivTable, user, host, dbName, tblName)
		_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
	}
	return err
}

func (e *RevokeExec) revokeColumnPriv(ctx context.Context, internalSession sessionctx.Context, priv *ast.PrivElem, user, host string) error {
	ctx = kv.WithInternalSourceType(ctx, kv.InternalTxnPrivilege)
	dbName, tbl, err := getTargetSchemaAndTable(ctx, e.Ctx(), e.Level.DBName, e.Level.TableName, e.is)
	if err != nil {
		return err
	}
	sql := new(strings.Builder)
	for _, c := range priv.Cols {
		col := table.FindCol(tbl.Cols(), c.Name.L)
		if col == nil {
			return errors.Errorf("Unknown column: %s", c)
		}

		sql.Reset()
		sqlescape.MustFormatSQL(sql, "UPDATE %n.%n SET ", mysql.SystemDB, mysql.ColumnPrivTable)
		isDelRow, err := composeColumnPrivUpdateForRevoke(internalSession, sql, priv.Priv, user, host, dbName, tbl.Meta().Name.O, col.Name.O)
		if err != nil {
			return err
		}
		sqlescape.MustFormatSQL(sql, " WHERE User=%? AND Host=%? AND DB=%? AND Table_name=%? AND Column_name=%?", user, host, dbName, tbl.Meta().Name.O, col.Name.O)

		_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
		if err != nil {
			return err
		}

		if isDelRow {
			sql.Reset()
			sqlescape.MustFormatSQL(sql, "DELETE FROM %n.%n WHERE User=%? AND Host=%? AND DB=%? AND Table_name=%? AND Column_name=%?", mysql.SystemDB, mysql.ColumnPrivTable, user, host, dbName, tbl.Meta().Name.O, col.Name.O)
			_, err = internalSession.GetSQLExecutor().ExecuteInternal(ctx, sql.String())
			if err != nil {
				return err
			}
			break
		}
		//TODO Optimized for batch, one-shot.
	}
	return nil
}

func privUpdateForRevoke(cur []string, priv mysql.PrivilegeType) ([]string, error) {
	p, ok := mysql.Priv2SetStr[priv]
	if !ok {
		return nil, errors.Errorf("Unknown priv: %v", priv)
	}
	cur = deleteFromSet(cur, p)
	return cur, nil
}

func composeTablePrivUpdateForRevoke(ctx sessionctx.Context, sql *strings.Builder, priv mysql.PrivilegeType, name string, host string, db string, tbl string) (bool, error) {
	var newTablePriv, newColumnPriv []string

	currTablePriv, currColumnPriv, err := getTablePriv(ctx, name, host, db, tbl)
	if err != nil {
		return false, err
	}

	if priv == mysql.AllPriv {
		// Revoke ALL does not revoke the Grant option,
		// so we only need to check if the user previously had this.
		tmp := SetFromString(currTablePriv)
		for _, p := range tmp {
			if p == mysql.Priv2SetStr[mysql.GrantPriv] {
				newTablePriv = []string{mysql.Priv2SetStr[mysql.GrantPriv]}
			}
		}
	} else {
		newTablePriv = SetFromString(currTablePriv)
		newTablePriv, err = privUpdateForRevoke(newTablePriv, priv)
		if err != nil {
			return false, err
		}

		newColumnPriv = SetFromString(currColumnPriv)
		newColumnPriv, err = privUpdateForRevoke(newColumnPriv, priv)
		if err != nil {
			return false, err
		}
	}
	sqlescape.MustFormatSQL(sql, `Table_priv=%?, Column_priv=%?, Grantor=%?`, strings.Join(newTablePriv, ","), strings.Join(newColumnPriv, ","), ctx.GetSessionVars().User.String())
	return len(newTablePriv) == 0, nil
}

func composeColumnPrivUpdateForRevoke(ctx sessionctx.Context, sql *strings.Builder, priv mysql.PrivilegeType, name string, host string, db string, tbl string, col string) (bool, error) {
	var newColumnPriv []string

	if priv != mysql.AllPriv {
		currColumnPriv, err := getColumnPriv(ctx, name, host, db, tbl, col)
		if err != nil {
			return false, err
		}

		newColumnPriv = SetFromString(currColumnPriv)
		newColumnPriv, err = privUpdateForRevoke(newColumnPriv, priv)
		if err != nil {
			return false, err
		}
	}

	sqlescape.MustFormatSQL(sql, `Column_priv=%?`, strings.Join(newColumnPriv, ","))
	return len(newColumnPriv) == 0, nil
}
