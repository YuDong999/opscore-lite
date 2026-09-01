package db

// Embed the IANA time zone database so Windows deployments without zoneinfo
// files, such as Windows Server 2012, can still resolve locations like
// Asia/Shanghai when database drivers parse DSN time zone parameters.
import _ "time/tzdata"
