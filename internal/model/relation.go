package model

type Relation struct {
	ID      int64
	RelType string // PARENT, CHILD, SIBLING
	RelUID  string // UID of related component
}
