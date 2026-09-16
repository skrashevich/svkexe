# Database Package

This package provides database operations for the Shelley AI coding agent using SQLite and sqlc.

## Architecture

The database contains two main entities:

- **Conversations**: Represent individual chat sessions with the AI agent
- **Messages**: Individual messages within conversations

## Schema Guidelines

Use SQLite `CHECK` constraints sparingly. They are painful to change: relaxing
or expanding one usually means rebuilding the table in a migration. Prefer
application-level validation for enums and other values that are likely to grow.

## Testing

Run tests with:

```bash
go test -v ./db/...
```

The tests use in-memory SQLite databases and cover all major operations including:

- CRUD operations for conversations and messages
- Pagination and search functionality
- JSON data marshalling/unmarshalling
- Foreign key constraints
- Transaction handling

## Code Generation

This package uses [sqlc](https://sqlc.dev/) to generate type-safe Go code from SQL queries.

To regenerate code after modifying SQL:

```bash
go run github.com/sqlc-dev/sqlc/cmd/sqlc generate
```
