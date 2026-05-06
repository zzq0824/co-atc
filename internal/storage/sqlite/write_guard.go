package sqlite

import (
	"strings"
	"sync"
)

// sqliteWriteMu 在共享同一数据库句柄的所有 sqlite 存储模块之间序列化写操作。
// SQLite 一次只允许一个写者,因此跨模块的写操作应使用相同的锁,
// 以避免 SQLITE_BUSY 争用。
var sqliteWriteMu sync.Mutex

func lockSQLiteWrite() {
	sqliteWriteMu.Lock()
}

func unlockSQLiteWrite() {
	sqliteWriteMu.Unlock()
}

func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}
