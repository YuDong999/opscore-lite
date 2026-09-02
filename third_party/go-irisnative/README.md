# go-irisnative

A Golang driver for InterSystems IRIS that implements `database/sql`.

> Project status: **alpha**. API may change. Feedback and PRs welcome.

---

## Installation

```bash
# replace the module path with the final repo path when published
go get github.com/caretdev/go-irisnative
```

Register the driver by importing it for side‑effects:

```go
import (
  "database/sql"
  _ "github.com/caretdev/go-irisnative" // registers driver as "iris"
)
```

## DSN formats

The driver accepts a URL-style DSN (recommended) or key=value pairs.

**URL style**

```
iris://user:password@host:1972/NAMESPACE?
```

* `host` — IRIS hostname or IP
* `1972` — superserver port (default)
* `Namespace` — IRIS namespace (e.g., `USER`)

---

## Quick start (database/sql)

```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/caretdev/go-irisnative"
)

func main() {
	dsn := "iris://_SYSTEM:SYS@localhost:1972/USER"
	db, err := sql.Open("iris", dsn)
	if err != nil { log.Fatal(err) }
	defer db.Close()

	// Connection pool tuning
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = db.ExecContext(ctx, `DROP TABLE IF EXISTS demo_person`)
	if err != nil { log.Fatal("drop table:", err) }

	// 1) Create a table (id INT PRIMARY KEY, name VARCHAR(80))
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS demo_person (
		id INT PRIMARY KEY,
		name VARCHAR(80) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil { log.Fatal("create table:", err) }

	// 2) Insert with placeholders
	res, err := db.ExecContext(ctx, `INSERT INTO demo_person(id, name) VALUES(?, ?)`, 1, "Alice")
	if err != nil { log.Fatal("insert:", err) }
	if n, _ := res.RowsAffected(); n > 0 { fmt.Println("inserted:", n) }

	// 3) Query rows
	rows, err := db.QueryContext(ctx, `SELECT id, name, created_at FROM demo_person ORDER BY id`)
	if err != nil { log.Fatal("query:", err) }
	defer rows.Close()

	for rows.Next() {
		var (
			id int
			name string
			createdAt time.Time
		)
		if err := rows.Scan(&id, &name, &createdAt); err != nil { log.Fatal(err) }
		fmt.Printf("row: id=%d name=%s created_at=%s\n", id, name, createdAt.Format(time.RFC3339))
	}
	if err := rows.Err(); err != nil { log.Fatal(err) }

	// 4) Prepared statement
	stmt, err := db.PrepareContext(ctx, `UPDATE demo_person SET name=? WHERE id=?`)
	if err != nil { log.Fatal("prepare:", err) }
	defer stmt.Close()
	if _, err := stmt.ExecContext(ctx, "Alice Updated", 1); err != nil { log.Fatal("update:", err) }

	// 5) Transaction example
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil { log.Fatal("begin tx:", err) }
	if _, err := tx.ExecContext(ctx, `INSERT INTO demo_person(id, name) VALUES(?, ?)`, 2, "Bob"); err != nil {
		tx.Rollback()
		log.Fatal("tx insert:", err)
	}
	if err := tx.Commit(); err != nil { log.Fatal("commit:", err) }
}
```

### Query single value helper

```go
var count int
if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM demo_person`).Scan(&count); err != nil {
	log.Fatal(err)
}
fmt.Println("count=", count)
```

---

## Using with `sqlx`

`sqlx` adds nice helpers over `database/sql` like struct scanning and named queries.

```bash
go get github.com/jmoiron/sqlx
```

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	_ "github.com/caretdev/go-irisnative" // driver
	"github.com/jmoiron/sqlx"
)

type Person struct {
	ID        int       `db:"id"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

func create(ctx context.Context, db *sqlx.DB) {
	drop(ctx, db)
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS demo_person (
		id INT PRIMARY KEY,
		name VARCHAR(80) NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		panic(err)
	}
}

func drop(ctx context.Context, db *sqlx.DB) {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS demo_person`)
	if err != nil {
		panic(err)
	}
}

func main() {
	ctx := context.Background()
	dsn := "iris://_SYSTEM:SYS@localhost:1972/USER"
	db := sqlx.MustConnect("iris", dsn)
	defer db.Close()

	create(ctx, db)
	defer drop(ctx, db)

	// Struct-based insert with NamedExec
	p := Person{ID: 3, Name: "Carol"}
	_, err := db.NamedExecContext(ctx,
		`INSERT INTO demo_person(id, name) VALUES(:id, :name)`, p,
	)
	if err != nil {
		log.Fatal("named insert:", err)
	}

	// Select into slice of structs
	var people []Person
	if err := db.SelectContext(ctx, &people, `SELECT id, name, created_at FROM demo_person ORDER BY id`); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("people: %#v\n", people)

	// Get a single struct
	var one Person
	if err := db.GetContext(ctx, &one, `SELECT id, name, created_at FROM demo_person WHERE id=?`, people[0].ID); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("one: %+v\n", one)

	// Named query with IN (sqlx.In)
	ids := []int{1, 2, 3}
	q, args, err := sqlx.In(`SELECT id, name FROM demo_person WHERE id IN (?)`, ids)
	if err != nil {
		log.Fatal(err)
	}
	q = db.Rebind(q) // ensure driver-specific bindvars
	rows, err := db.QueryxContext(ctx, q, args...)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			log.Fatal(err)
		}
		fmt.Println(id, name)
	}
}
```

---

## Placeholders & rebind

* The driver uses `?` positional placeholders.
* With `sqlx`, **always** call `db.Rebind(q)` after `sqlx.In(...)` to adapt placeholders.

---

## Context, timeouts & cancellations

All examples use `Context`. Set sensible timeouts to avoid runaway queries:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
```

---

## Error handling tips

* Check `rows.Err()` after iteration.
* Prefer `ExecContext`/`QueryContext` to ensure timeouts are respected.
* Wrap errors with operation context (e.g., `fmt.Errorf("create table: %w", err)`).

---

## Testing locally

1. Start IRIS and ensure SQL is enabled for your namespace (e.g., `USER`).
2. Create a SQL user with privileges to connect and create tables.
3. Verify connectivity using the DSN shown above.

---

## Compatibility

* Go: 1.21+
* InterSystems IRIS: 2025.1+

---

## License

MIT

---

## Contributing

* Run `go vet` and `go test ./...` before submitting PRs.
* Add tests for new behaviors.
* Document any DSN parameters you introduce.
