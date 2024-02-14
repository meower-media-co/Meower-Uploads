package main

import (
	"encoding/json"
	"log"
)

func startPubSubListener() {
	pubsub := rdb.Subscribe(ctx, "uploads")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var event map[string]string
		err := json.Unmarshal([]byte(msg.Payload), &event)
		if err != nil {
			log.Println(err)
			continue
		}

		switch event["op"] {
		case "claim_icon":
			if event["id"] == "" && event["resource"] == "" {
				continue
			}

			_, err = db.Exec("UPDATE icons SET used_by=$1 WHERE id=$2", event["resource"], event["id"])
			if err != nil {
				log.Println(err)
				continue
			}
		case "claim_attachment":
			if event["id"] == "" || event["resource"] == "" {
				continue
			}

			_, err = db.Exec("UPDATE attachments SET used_by=$1 WHERE id=$2", event["resource"], event["id"])
			if err != nil {
				log.Println(err)
				continue
			}
		case "unclaim_icon":
			if event["id"] != "" {
				_, err = db.Exec("UPDATE icons SET used_by='' WHERE id=$1", event["id"])
				if err != nil {
					log.Println(err)
				}
			}
			if event["uploader"] != "" {
				_, err = db.Exec("UPDATE icons SET used_by='' WHERE uploader=$1", event["uploader"])
				if err != nil {
					log.Println(err)
				}
			}
		case "unclaim_attachment":
			if event["id"] != "" {
				_, err = db.Exec("UPDATE attachments SET used_by='' WHERE id=$1", event["id"])
				if err != nil {
					log.Println(err)
				}
			}
			if event["uploader"] != "" {
				_, err = db.Exec("UPDATE attachments SET used_by='' WHERE uploader=$1", event["uploader"])
				if err != nil {
					log.Println(err)
				}
			}
		case "run_background_tasks":
			go cleanupIcons()
			go cleanupAttachments()
		}
	}
}
