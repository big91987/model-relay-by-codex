CREATE TABLE models(alias TEXT PRIMARY KEY,provider TEXT NOT NULL,upstream TEXT NOT NULL,kind TEXT NOT NULL, input_bound INTEGER NOT NULL DEFAULT 0,output_bound INTEGER NOT NULL DEFAULT 4096,input_rate INTEGER NOT NULL DEFAULT -1,cached_rate INTEGER NOT NULL DEFAULT -1,output_rate INTEGER NOT NULL DEFAULT -1,temperature INTEGER NOT NULL DEFAULT 1,favorite INTEGER NOT NULL DEFAULT 0);
INSERT INTO models(alias,provider,upstream,kind,temperature) VALUES ('deepseek-v4-flash','deepseek','deepseek-v4-flash','chat',1),('deepseek-v4-pro','deepseek','deepseek-v4-pro','chat',1),('embedding-3','bigmodel','embedding-3','embedding',0);
CREATE TABLE keys(id TEXT PRIMARY KEY,name TEXT NOT NULL,digest TEXT NOT NULL UNIQUE,allowed TEXT NOT NULL,budget INTEGER NOT NULL,rpm INTEGER NOT NULL,parallel INTEGER NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE calls(id TEXT PRIMARY KEY,created INTEGER NOT NULL,key_id TEXT NOT NULL,model TEXT NOT NULL,provider TEXT NOT NULL,state TEXT NOT NULL,hold INTEGER NOT NULL DEFAULT 0,cost INTEGER,input INTEGER,output INTEGER,duration INTEGER NOT NULL DEFAULT 0,note TEXT NOT NULL DEFAULT '',up_id TEXT NOT NULL DEFAULT '');
CREATE INDEX calls_key ON calls(key_id,state,created);
CREATE INDEX calls_time ON calls(created);
CREATE TABLE sessions(digest TEXT PRIMARY KEY,expires INTEGER NOT NULL);
CREATE TABLE jobs(id TEXT PRIMARY KEY,kind TEXT NOT NULL,provider TEXT NOT NULL,model TEXT NOT NULL,state TEXT NOT NULL,remote TEXT NOT NULL DEFAULT '',asset TEXT NOT NULL DEFAULT '',voice TEXT NOT NULL DEFAULT '',note TEXT NOT NULL DEFAULT '',created INTEGER NOT NULL,usage TEXT NOT NULL DEFAULT '');
PRAGMA user_version=1;
