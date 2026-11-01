# Synthetic Codex rollouts in the record shapes of a real one (0.159.0-alpha)
# and of an older one with no turn markers. Made-up content.
#   python3 codex.py ..
import json,sys,os
out=sys.argv[1]
T=[0]
def ts():
    T[0]+=700; s,ms=divmod(T[0],1000); return '2026-01-02T04:%02d:%02d.%03dZ'%(s//60%60,s%60,ms)
def rec(typ,payload): return {'timestamp':ts(),'type':typ,'payload':payload}
SID='01a00000-0000-7000-8000-00000000c0de'
def modern():
    r=[]
    r.append(rec('session_meta',{'id':SID,'session_id':SID,'timestamp':ts(),'cwd':'/home/dev/project','originator':'codex_cli_rs','cli_version':'0.159.0-alpha.12.1','source':'cli','model_provider':'openai','base_instructions':{'text':'fixture instructions'},'context_window':None,'creator_account_id':None,'creator_user_id':None,'history_mode':'full','runtime_workspace_roots':[],'thread_source':None}))
    r.append(rec('event_msg',{'type':'task_started','turn_id':'turn-1','root_turn_id':'turn-1','started_at':1,'model_context_window':1000,'collaboration_mode_kind':'default'}))
    r.append(rec('turn_context',{'turn_id':'turn-1','root_turn_id':'turn-1','cwd':'/home/dev/project','model':'fixture-model','approval_policy':'on-request','summary':'auto','effort':'medium'}))
    r.append(rec('response_item',{'type':'message','id':'msg-dev','role':'developer','content':[{'type':'input_text','text':'<permissions instructions>fixture</permissions instructions>'}]}))
    r.append(rec('response_item',{'type':'message','id':'msg-env','role':'user','content':[{'type':'input_text','text':'<environment_context>\n  <cwd>/home/dev/project</cwd>\n</environment_context>'}]}))
    r.append(rec('response_item',{'type':'message','id':'msg-u1','role':'user','content':[{'type':'input_text','text':'Fix the typo in README.md.'}]}))
    r.append(rec('event_msg',{'type':'item_completed','thread_id':SID,'turn_id':'turn-1','item':{'type':'UserMessage','id':'item-u1','content':[{'type':'text','text':'Fix the typo in README.md.'}]},'started_at_ms':1,'completed_at_ms':2}))
    r.append(rec('response_item',{'type':'reasoning','id':'rs-1','summary':[{'type':'summary_text','text':'Looking for the typo.'}],'encrypted_content':'ZW5j'}))
    r.append(rec('response_item',{'type':'function_call','name':'exec_command','arguments':json.dumps({'cmd':'grep -n teh README.md','workdir':'/home/dev/project'}),'call_id':'call-1'}))
    r.append(rec('response_item',{'type':'function_call_output','call_id':'call-1','output':json.dumps({'output':'3:teh quick fox\n','metadata':{'exit_code':0,'duration_seconds':0.1}})}))
    patch='*** Begin Patch\n*** Update File: README.md\n@@\n-teh quick fox\n+the quick fox\n*** End Patch\n'
    r.append(rec('response_item',{'type':'custom_tool_call','status':'completed','call_id':'call-2','name':'apply_patch','input':patch}))
    r.append(rec('response_item',{'type':'custom_tool_call_output','call_id':'call-2','output':json.dumps({'output':'Success. Updated the following files:\nM README.md\n','metadata':{'exit_code':0}})}))
    r.append(rec('response_item',{'type':'message','id':'msg-a1','role':'assistant','phase':'final_answer','content':[{'type':'output_text','text':'Fixed **teh** → **the** in README.md.'}]}))
    r.append(rec('event_msg',{'type':'item_completed','thread_id':SID,'turn_id':'turn-1','item':{'type':'AgentMessage','id':'msg-a1','content':[{'type':'Text','text':'Fixed **teh** → **the** in README.md.'}]},'started_at_ms':1,'completed_at_ms':2}))
    r.append(rec('event_msg',{'type':'token_count','info':None,'rate_limits':None}))
    r.append(rec('event_msg',{'type':'task_complete','turn_id':'turn-1','last_agent_message':'Fixed.','duration_ms':4200,'started_at':1,'completed_at':5,'time_to_first_token_ms':100}))
    r.append(rec('event_msg',{'type':'task_started','turn_id':'turn-2','root_turn_id':'turn-2','started_at':6}))
    r.append(rec('response_item',{'type':'message','id':'msg-u2','role':'user','content':[{'type':'input_text','text':'Now run the tests.'}]}))
    r.append(rec('response_item',{'type':'function_call','name':'exec_command','arguments':json.dumps({'cmd':'make test'}),'call_id':'call-3'}))
    r.append(rec('response_item',{'type':'function_call_output','call_id':'call-3','output':json.dumps({'output':'FAIL\n','metadata':{'exit_code':2}})}))
    r.append(rec('event_msg',{'type':'turn_aborted','turn_id':'turn-2','reason':'interrupted'}))
    return r
def legacy():
    r=[]
    r.append({'timestamp':ts(),'type':'session_meta','payload':{'id':'0199aaaa-0000-7000-8000-000000000001','timestamp':ts(),'cwd':'/home/dev/project','originator':'codex_cli_rs','cli_version':'0.46.0','instructions':None}})
    r.append(rec('response_item',{'type':'message','role':'user','content':[{'type':'input_text','text':'<user_instructions>\nfixture\n</user_instructions>'}]}))
    r.append(rec('response_item',{'type':'message','role':'user','content':[{'type':'input_text','text':'What is in src?'}]}))
    r.append(rec('event_msg',{'type':'user_message','message':'What is in src?','kind':'plain'}))
    r.append(rec('response_item',{'type':'reasoning','summary':[],'content':None,'encrypted_content':'ZW5j'}))
    r.append(rec('response_item',{'type':'function_call','name':'shell','arguments':json.dumps({'command':['bash','-lc','ls src'],'workdir':'/home/dev/project'}),'call_id':'call_a'}))
    r.append(rec('response_item',{'type':'function_call_output','call_id':'call_a','output':'main.go\nutil.go\n'}))
    r.append(rec('response_item',{'type':'message','role':'assistant','content':[{'type':'output_text','text':'Two files: main.go and util.go.'}]}))
    r.append(rec('event_msg',{'type':'agent_message','message':'Two files: main.go and util.go.'}))
    return r
for name,fn in (('codex-0.159.jsonl',modern),('codex-0.46.jsonl',legacy)):
    T[0]=0
    with open(os.path.join(out,name),'w') as f:
        for x in fn(): f.write(json.dumps(x,ensure_ascii=False,separators=(',',':'))+'\n')
