#!/usr/bin/env python3
import json,os,sys,subprocess,time
kind=os.path.basename(sys.argv[0]);session='fixture-'+kind
mode=os.environ.get('GNAT_RPC_MODE','')
def emit(v): print(json.dumps(v),flush=True)
def reply(m,r):emit({'jsonrpc':'2.0','id':m['id'],'result':r})
def fail(m):emit({'jsonrpc':'2.0','id':m['id'],'error':{'code':-32000,'message':'saved session unavailable'}})
def config(model='fixture-model',effort='high'):
 return {'configOptions':[{'id':'model','currentValue':model,'options':[{'group':'models','options':[{'value':'fixture-model'}]}]},{'id':'effort','currentValue':effort,'options':[{'value':'high'}]}]}
def note(method,p):emit({'jsonrpc':'2.0','method':method,'params':p})
for line in sys.stdin:
 m=json.loads(line);method=m.get('method');p=m.get('params',{})
 with open(os.environ['GNAT_TEST_TRACE'],'a') as f:f.write(json.dumps({'kind':kind,'cwd':os.getcwd(),'message':m})+'\n')
 if method=='initialize':
  caps={'protocolVersion':1,'agentCapabilities':{'mcpCapabilities':{'http':mode!='incompatible'},'sessionCapabilities':{'resume':{}}}}
  reply(m,caps);continue
 if method=='initialized':continue
 if method=='account/read':reply(m,{'account':None if mode=='unauthenticated' else {'type':'chatgpt'},'requiresOpenaiAuth':True});continue
 if method=='model/list':reply(m,{'data':[{'id':'fixture-model','model':'fixture-model','supportedReasoningEfforts':[{'reasoningEffort':'high'}]}]});continue
 if method in ['thread/start','thread/resume','session/new','session/resume']:
  if mode=='bad-resume' and method.endswith('resume'):fail(m);continue
  session=p.get('threadId',p.get('sessionId',session))
  if mode=='wrong-session':session='wrong'
  r={'thread':{'id':session}} if kind=='codex' else dict(config(),sessionId=session)
  reply(m,r);continue
 if method=='session/set_config_option':
  reply(m,config());continue
 if method in ['turn/interrupt','session/cancel']:
  if mode=='ignore-cancel':continue
  if kind=='codex':
   if 'id' not in m:sys.exit('interrupt requires request ID')
   note('turn/completed',{'threadId':session,'turn':{'id':'turn-1','status':'interrupted'}})
  else:reply(prompt,{'stopReason':'cancelled'})
  continue
 if method not in ['turn/start','session/prompt']:continue
 prompt=m
 if mode=='invalid':print('invalid JSON',flush=True);continue
 if mode=='eof':sys.exit(1)
 if kind=='codex':
  note('item/agentMessage/delta',{'threadId':session,'turnId':'unrelated','itemId':'bad','delta':'wrong turn'})
  note('item/agentMessage/delta',{'threadId':session,'turnId':'turn-1','itemId':'reply','delta':'hello'})
  reply(m,{'turn':{'id':'turn-1'}})
 else:note('session/update',{'sessionId':session,'update':{'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':'hello'}}})
 if mode in ['block','ignore-cancel','orphan']:
  child=subprocess.Popen(['sleep','1000'])
  with open(os.environ['GNAT_TEST_CHILD'],'w') as f:f.write(str(child.pid))
  if mode=='orphan':sys.exit(1)
  continue
 if kind=='codex':
  def item(method,v):note(method,{'threadId':session,'turnId':'turn-1','item':v})
  item('item/completed',{'id':'reply','type':'agentMessage','text':'hello'})
  item('item/started',{'id':'tool','type':'mcpToolCall','server':'gnat','tool':'list_agents','arguments':{}})
  item('item/completed',{'id':'tool','type':'mcpToolCall','server':'gnat','tool':'list_agents','result':{'ok':True}})
  for total,last in [(110,10),(125,15),(125,15)]:note('thread/tokenUsage/updated',{'threadId':session,'turnId':'turn-1','tokenUsage':{'total':{'inputTokens':total,'cachedInputTokens':20,'outputTokens':30+total-110},'last':{'inputTokens':last,'cachedInputTokens':0,'outputTokens':5}}})
  note('turn/completed',{'threadId':session,'turn':{'id':'turn-1','status':'failed' if mode=='failed' else 'completed','error':{'message':'provider failed'}}})
 else:
  def update(v):note('session/update',{'sessionId':session,'update':v})
  emit({'jsonrpc':'2.0','id':'permission','method':'session/request_permission','params':{'sessionId':session,'options':[{'optionId':'allow','kind':'allow_once'}]}})
  response=json.loads(sys.stdin.readline())
  with open(os.environ['GNAT_TEST_TRACE'],'a') as f:f.write(json.dumps({'message':response})+'\n')
  update({'sessionUpdate':'tool_call','toolCallId':'tool','title':'list_agents','rawInput':{}})
  update({'sessionUpdate':'tool_call_update','toolCallId':'tool','status':'completed','rawOutput':'team'})
  update({'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':'child'},'_meta':{'opencode/child-session':{'id':'nested'}}})
  reply(m,{'stopReason':'max_tokens' if mode=='failed' else 'end_turn','usage':{'inputTokens':25,'outputTokens':20}})
