package trajectory

import (
	"database/sql/driver"
	"errors"
	"modernc.org/sqlite"
)

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("traj_text", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return v, nil // only during the bounded existing-column migration
		case []byte:
			raw, err := decodeStored(v)
			return string(raw), err
		default:
			return nil, errors.New("invalid trajectory search payload")
		}
	})
	sqlite.MustRegisterDeterministicScalarFunction("traj_size", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return int64(len(v)), nil
		case []byte:
			return int64(storedLogicalSize(v)), nil
		default:
			return nil, errors.New("invalid trajectory search payload")
		}
	})
}
