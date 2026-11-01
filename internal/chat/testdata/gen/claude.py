# Synthetic Claude Code transcripts: the record shapes (key names and types,
# schema.json) of each CLI version seen on a real machine, with made-up
# content.
#   python3 claude.py .. 2.1.231 2.1.270 2.1.284 2.1.293
import json,sys,os
sch=json.load(open(os.path.join(os.path.dirname(__file__),'schema.json')))
out=sys.argv[1]
SEM={'isMeta','isSynthetic','isCompactSummary','isApiErrorMessage','interruptedMessageId','toolUseResult',
 'sourceToolAssistantUUID','sourceToolUseID','error','apiErrorStatus','truncatedAfterOutput','toolDenialKind',
 'queueOrigin','queueSkipAttachments','queueTranscriptOnly','turnCompanion','turnOrigin','turnPosition',
 'permissionDecision','quotaLimits','isVisibleInTranscriptOnly','logicalParentUuid','compactMetadata',
 'attributionMcpServer','attributionMcpTool','attributionPlugin','attributionSkill','wireToolInputs','origin','promptSource',
 'thinkingDurationMs','advisorModel','perTurnEffort','content','level','commandRun','durationMs','messageCount',
 'pendingBackgroundAgentCount','hookInfos','hookErrors','hookCount','hasOutput','preventedContinuation','stopReason','toolUseID','hookAdditionalContext','sessionKind'}
FILL={'cwd':'/home/dev/project','gitBranch':'main','sessionId':'11111111-2222-4333-8444-555555555555',
 'session_id':'11111111-2222-4333-8444-555555555555','entrypoint':'cli','userType':'external','slug':'quiet-fixture-otter',
 'permissionMode':'default','effort':'high','requestId':'req_fixture','promptId':'p0000000-0000-4000-8000-000000000000'}
DEF={'s':'x','b':False,'n':0,'o':{},'a':[],'z':None}
n=[0]
def uid():
    n[0]+=1; return '00000000-0000-4000-8000-%012d'%n[0]
t=[0]
def ts():
    t[0]+=1500; s,ms=divmod(t[0],1000); return '2026-01-02T03:%02d:%02d.%03dZ'%(s//60%60,s%60,ms)
def gen(v):
    s=sch[v]; recs=[]; last=[None]
    def base(kind, extra):
        k=kind if kind in s else None
        keys=s.get(kind,{})
        r={}
        for key,ty in keys.items():
            if key in SEM: continue
            r[key]=FILL.get(key,DEF[ty])
        r.update({'parentUuid':last[0],'isSidechain':False,'uuid':uid(),'timestamp':ts(),'version':v})
        if kind.startswith('system:'): r['type']='system'; r['subtype']=kind.split(':')[1]
        else: r['type']=kind
        r.update(extra); last[0]=r['uuid']; recs.append(r); return r
    def user(content, **kw): return base('user',dict(message={'role':'user','content':content},**kw))
    msgid=[0]
    def asst(blocks, stop, **kw):
        msgid[0]+=1
        m={'model':'claude-fixture-1','id':'msg_fixture%03d'%msgid[0],'type':'message','role':'assistant','content':blocks,
           'stop_reason':stop,'stop_sequence':None,'usage':{'input_tokens':10,'output_tokens':5}}
        return base('assistant',dict(message=m,**kw))
    def asst_split(blocks, stop):
        # Recent versions write one record per content block of one message.
        msgid[0]+=1; mid='msg_fixture%03d'%msgid[0]; out=[]
        for i,b in enumerate(blocks):
            m={'model':'claude-fixture-1','id':mid,'type':'message','role':'assistant','content':[b],'stop_reason':stop,'stop_sequence':None,'usage':{'input_tokens':10,'output_tokens':5}}
            ex={'apiBlockIndex':i} if 'apiBlockIndex' in s.get('assistant',{}) else {}
            out.append(base('assistant',dict(message=m,**ex)))
        return out
    recs.append({'type':'permission-mode','permissionMode':'default','sessionId':FILL['sessionId']})
    recs.append({'type':'file-history-snapshot','messageId':'00000000-0000-4000-8000-999999999999','snapshot':{'messageId':'00000000-0000-4000-8000-999999999999','trackedFileBackups':{},'timestamp':ts()},'isSnapshotUpdate':False})
    # Turn 1: a prompt, an edit, a long command output, an answer.
    p1=user('Rename the greeting in hello.txt and list the directory.',promptId='p1')
    recs.append({'type':'attachment','attachment':{'type':'fixture'},'parentUuid':p1['uuid'],'uuid':uid(),'timestamp':ts(),'sessionId':FILL['sessionId']})
    sig={'type':'thinking','thinking':'','signature':'c2lnbmF0dXJl'}
    think={'type':'thinking','thinking':'The file has one line; an Edit will do.','signature':'c2lnbmF0dXJl'}
    edit={'type':'tool_use','id':'toolu_fixture_edit','name':'Edit','input':{'file_path':'/home/dev/project/hello.txt','old_string':'hello','new_string':'hi'},'caller':{'type':'direct'}}
    asst_split([think if v!='2.1.293' else sig,{'type':'text','text':"I'll change the greeting."},edit],'tool_use')
    user([{'tool_use_id':'toolu_fixture_edit','type':'tool_result','content':'The file /home/dev/project/hello.txt has been updated successfully.'}],
         toolUseResult={'filePath':'/home/dev/project/hello.txt','oldString':'hello','newString':'hi','originalFile':'hello\nworld\n',
           'structuredPatch':[{'oldStart':1,'oldLines':2,'newStart':1,'newLines':2,'lines':['-hello','+hi',' world']}],'userModified':False,'replaceAll':False},
         sourceToolAssistantUUID=last[0])
    bash={'type':'tool_use','id':'toolu_fixture_bash','name':'Bash','input':{'command':'ls -la','description':'List the directory'},'caller':{'type':'direct'}}
    asst_split([bash],'tool_use')
    longout='\n'.join('-rw-r--r--  1 dev  staff  %5d  Jan  2 03:04 file%04d.txt'%(i*7,i) for i in range(120))
    user([{'tool_use_id':'toolu_fixture_bash','type':'tool_result','content':longout,'is_error':False}],
         toolUseResult={'stdout':longout,'stderr':'','interrupted':False,'isImage':False,'noOutputExpected':False},sourceToolAssistantUUID=last[0])
    asst_split([{'type':'text','text':'Done: **hello** is now **hi**, and the directory has 120 files.'}],'end_turn')
    if 'system:stop_hook_summary' in s: base('system:stop_hook_summary',{'hookCount':1,'hookInfos':[],'hookErrors':[],'preventedContinuation':False,'stopReason':'','hasOutput':False,'level':'suggestion'})
    if 'system:turn_duration' in s: base('system:turn_duration',{'durationMs':12345,'messageCount':9,'isMeta':False})
    # A local command.
    user('<local-command-caveat>Caveat: The messages below were generated by the user while running local commands. DO NOT respond to these messages or otherwise consider them in your response unless the user explicitly asks you to.</local-command-caveat>',isMeta=True)
    cmd=user('<command-name>/context</command-name>\n            <command-message>context</command-message>\n            <command-args></command-args>')
    if 'system:local_command' in s:
        base('system:local_command',{'content':'<local-command-stdout>\x1b[1mContext Usage\x1b[22m\nfixture-model · 18.3k/200k tokens (9%)</local-command-stdout>','level':'info','isMeta':False})
    else:
        user('<local-command-stdout>\x1b[1mContext Usage\x1b[22m\nfixture-model · 18.3k/200k tokens (9%)</local-command-stdout>')
    # Turn 2: a subagent, then the user stops it.
    p2=user([{'type':'text','text':'Find every TODO with a subagent.'}])
    agent={'type':'tool_use','id':'toolu_fixture_agent','name':'Agent','input':{'description':'Find TODOs','subagent_type':'Explore','prompt':'Search the repository for TODO comments.'},'caller':{'type':'direct'}}
    asst_split([agent],'tool_use')
    user([{'tool_use_id':'toolu_fixture_agent','type':'tool_result','content':[{'type':'text','text':'Found 3 TODOs.'}]}],
         toolUseResult={'status':'completed','agentId':'a0fixture00000001','content':[{'type':'text','text':'Found 3 TODOs.'}],'totalDurationMs':900},sourceToolAssistantUUID=last[0])
    bash2={'type':'tool_use','id':'toolu_fixture_bash2','name':'Bash','input':{'command':'sleep 100'},'caller':{'type':'direct'}}
    asst_split([bash2],'tool_use')
    user([{'type':'tool_result','content':'The user doesn\'t want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.','is_error':True,'tool_use_id':'toolu_fixture_bash2'}],
         toolUseResult='User rejected tool use',sourceToolAssistantUUID=last[0])
    user([{'type':'text','text':'[Request interrupted by user for tool use]'}])
    # Turn 3: interrupted while it thinks.
    p3=user('Explain the build.')
    a=asst_split([{'type':'text','text':'The build has two'}],'end_turn' if v=='2.1.231' else 'end_turn')
    user([{'type':'text','text':'[Request interrupted by user]'}],interruptedMessageId=a[0]['message']['id'])
    # Noise that is not a turn.
    user('<system-reminder>\nfixture reminder\n</system-reminder>',isMeta=True)
    user('<task-notification>\n<task-id>fixture</task-id>\n<status>completed</status>\n</task-notification>')
    if 'system:compact_boundary' in s:
        base('system:compact_boundary',{'content':'Conversation compacted','level':'info','compactMetadata':{'trigger':'manual','preTokens':1000}})
        user('This session is being continued from a previous conversation that ran out of context. Summary: fixture.',isCompactSummary=True,isVisibleInTranscriptOnly=True)
    # An API error the agent wrote down as its reply.
    p4=user('One more thing.')
    asst([{'type':'text','text':'API Error: 500 fixture'}],'stop_sequence',isApiErrorMessage=True)
    recs.append({'type':'ai-title','aiTitle':'Fixture conversation','sessionId':FILL['sessionId']})
    recs.append({'type':'last-prompt','lastPrompt':'One more thing.','sessionId':FILL['sessionId']})
    return recs
for v in sys.argv[2:]:
    n[0]=0; t[0]=0
    with open(os.path.join(out,'claude-%s.jsonl'%v),'w') as f:
        for r in gen(v): f.write(json.dumps(r,ensure_ascii=False,separators=(',',':'))+'\n')
