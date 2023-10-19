package main

type Upload struct {
	ID        string `bson:"_id" json:"id"`
	Hash      string `bson:"hash" json:"hash"`
	Fid       string `bson:"fid" json:"-"`
	Type      string `bson:"type" json:"type"`
	Filename  string `bson:"filename" json:"filename"`
	Mime      string `bson:"mime" json:"mime"`
	Size      int64  `bson:"size" json:"size"`
	CreatedAt int64  `bson:"created_at" json:"created_at"`
}
