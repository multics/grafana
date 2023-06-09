package ml

// DatasourceType is the string constant used as the datasource when the property is in Datasource.Type.
// Type in requests is used to identify what type of data source plugin the request belongs to.
const DatasourceType = "__ml__"

// DatasourceUID is the string constant used as the datasource name in requests
// to identify it as an expression command when use in Datasource.UID.
const DatasourceUID = DatasourceType

// IsDataSource checks if the uid points to ML node query
func IsDataSource(uid string) bool {
	return uid == DatasourceUID
}
