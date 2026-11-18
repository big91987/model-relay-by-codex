-- Estimated catalogue prices are informational. A confirmed invoice replaces the estimate.
ALTER TABLE calls ADD COLUMN estimate INTEGER;
ALTER TABLE calls ADD COLUMN bounded INTEGER NOT NULL DEFAULT 1;
UPDATE calls SET bounded=0 WHERE state IN ('pending','running') AND hold=0;
ALTER TABLE jobs ADD COLUMN custom_voice INTEGER NOT NULL DEFAULT 0;
UPDATE jobs SET state='failed' WHERE state='unknown' AND note LIKE '%HTTP 4%';
CREATE VIEW media_ledger AS
 SELECT id,created,model,provider,remote,
 CASE WHEN state IN ('processing','submitting') THEN 'running' ELSE 'pending' END AS billing_state,
 CASE
 WHEN state!='done' OR custom_voice=1 THEN NULL
 WHEN model='doubao-seedance-2-0-260128' THEN json_extract(nullif(usage,''),'$.completion_tokens')*46000
 WHEN model='MiniMax-H3-Lite' THEN json_extract(nullif(usage,''),'$.output_seconds')*160000000
 WHEN kind IN ('speech','stream','long','clone','design') THEN json_extract(nullif(usage,''),'$.usage_characters')*200000
 WHEN kind='music' AND model='music-3.0' THEN 1000000000
 END AS estimate,
 CASE WHEN note='' THEN '请按厂商账单核实媒体费用' ELSE note END AS note
 FROM jobs;
INSERT INTO calls(id,created,model,provider,key_id,state,up_id,estimate,note)
 SELECT id,created,model,provider,'console',billing_state,remote,estimate,note FROM media_ledger;
CREATE TRIGGER job_insert AFTER INSERT ON jobs BEGIN
 INSERT INTO calls(id,created,model,provider,key_id,state)
 VALUES(new.id,new.created,new.model,new.provider,'console','running');
END;
CREATE TRIGGER job_update AFTER UPDATE ON jobs BEGIN
 UPDATE calls SET (state,up_id,estimate,note)=(SELECT billing_state,remote,estimate,note FROM media_ledger WHERE id=new.id)
 WHERE id=new.id AND state!='actual';
END;
UPDATE calls SET estimate=input*2000+output*8000
 WHERE provider='deepseek' AND model='deepseek-v4-flash' AND cost IS NULL;
UPDATE calls SET estimate=input*500
 WHERE provider='bigmodel' AND model='embedding-3' AND cost IS NULL;
PRAGMA user_version=2;
