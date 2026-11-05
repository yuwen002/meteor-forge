package adminstore

import (
 "context"
 "fmt"
 "time"
)

// SecurityAuditEvent records authentication and administrator-account security events.
// It deliberately excludes passwords, session tokens, DSNs, and raw driver errors.
type SecurityAuditEvent struct {
 ID int64 `json:"id"`
 Actor string `json:"actor"`
 Subject string `json:"subject"`
 Action string `json:"action"`
 Outcome string `json:"outcome"`
 RequestSource string `json:"request_source,omitempty"`
 Details string `json:"details,omitempty"`
 OccurredAt time.Time `json:"occurred_at"`
}

func (s *Store) RecordSecurityAuditEvent(ctx context.Context,event SecurityAuditEvent) error {
 switch event.Action {
 case "login","logout","password_change","admin_create","admin_role_update","admin_delete":
 default: return fmt.Errorf("unsupported security audit action")
 }
 switch event.Outcome {
 case "succeeded","failed","denied":
 default: return fmt.Errorf("unsupported security audit outcome")
 }
 if len(event.Actor)>128 || len(event.Subject)>128 || len(event.RequestSource)>128 || len(event.Details)>256 {
  return fmt.Errorf("security audit field exceeds maximum length")
 }
 if event.OccurredAt.IsZero(){event.OccurredAt=time.Now().UTC()}else{event.OccurredAt=event.OccurredAt.UTC()}
 _,err:=s.db.ExecContext(ctx,`INSERT INTO admin_security_audit_events
  (actor,subject,action,outcome,request_source,details,occurred_at) VALUES(?,?,?,?,?,?,?)`,
  event.Actor,event.Subject,event.Action,event.Outcome,event.RequestSource,event.Details,event.OccurredAt.Format(time.RFC3339Nano))
 if err!=nil{return fmt.Errorf("record security audit event: %w",err)}
 return nil
}

func (s *Store) ListSecurityAuditEvents(ctx context.Context,limit,offset int)([]SecurityAuditEvent,int,error){
 if limit<=0||limit>200{limit=50};if offset<0{offset=0}
 var total int
 if err:=s.db.QueryRowContext(ctx,"SELECT COUNT(*) FROM admin_security_audit_events").Scan(&total);err!=nil{return nil,0,fmt.Errorf("count security audit events: %w",err)}
 rows,err:=s.db.QueryContext(ctx,`SELECT id,actor,subject,action,outcome,request_source,details,occurred_at
  FROM admin_security_audit_events ORDER BY julianday(occurred_at) DESC,id DESC LIMIT ? OFFSET ?`,limit,offset)
 if err!=nil{return nil,0,fmt.Errorf("list security audit events: %w",err)}
 defer rows.Close()
 items:=make([]SecurityAuditEvent,0)
 for rows.Next(){
  var item SecurityAuditEvent;var occurred string
  if err:=rows.Scan(&item.ID,&item.Actor,&item.Subject,&item.Action,&item.Outcome,&item.RequestSource,&item.Details,&occurred);err!=nil{return nil,0,err}
  item.OccurredAt,err=time.Parse(time.RFC3339Nano,occurred);if err!=nil{return nil,0,fmt.Errorf("parse security audit timestamp: %w",err)}
  items=append(items,item)
 }
 if err:=rows.Err();err!=nil{return nil,0,err}
 return items,total,nil
}
