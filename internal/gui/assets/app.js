(function(){
'use strict';

// Disable right-click context menu
document.addEventListener('contextmenu',function(e){e.preventDefault()});

function showScreen(s){var sc=document.querySelectorAll('.screen');for(var i=0;i<sc.length;i++)sc[i].classList.toggle('hidden',sc[i].id!=='screen-'+s)}
function setText(id,t){var e=document.getElementById(id);if(e)e.textContent=t}
function setWidth(id,p){var e=document.getElementById(id);if(e)e.style.width=p+'%'}
function fmtSp(m){if(m<=0)return '— MB/s';if(m<1)return Math.round(m*1024)+' KB/s';return m.toFixed(1)+' MB/s'}
function fmtEta(s){if(s<=0)return 'осталось —';if(s<60)return 'осталось '+s+' с';var m=Math.floor(s/60),r=s%60;return 'осталось '+m+':'+(r<10?'0':'')+r}
function fmtSz(m){if(m<1)return Math.round(m*1024)+' KB';return m.toFixed(1)+' MB'}
window.__updateState=function(p,seq){
  if(!p||!p.state)return;
  showScreen(p.state);
  if(p.version){setText('dl-version',p.version);setText('ua-version',p.version);setText('fu-version',p.version)}
  if(p.title)setText('welcome-path',p.title);
  if(p.state==='installed'){setText('inst-version','Версия '+p.version);setText('inst-path',p.title)}
  if(p.state==='downloading')setText('dl-title',p.isFirstRun?'Установка Syntee Helper':'Загрузка обновления');
  if(p.changelog!==undefined)setText('ua-changelog',p.changelog||'');
  if(p.errorMessage)setText('error-msg',p.errorMessage);
  if(p.title&&p.state==='done'){setText('done-title',p.title);setText('done-detail',p.detail||'')}
  if(p.progress){var pr=p.progress,pct=pr.percent||0;
    if(p.state==='downloading'){setWidth('dl-fill',pct);setText('dl-percent',pct+'%');setText('dl-speed',fmtSp(pr.speedMBps));setText('dl-size',fmtSz(pr.downloadedMB)+' / '+fmtSz(pr.totalMB));setText('dl-eta',fmtEta(pr.etaSec))}
    else if(p.state==='extracting'){setWidth('ex-fill',pct);setText('ex-percent',pct+'%')}
    else if(p.state==='forced_update'){setWidth('fu-fill',pct);setText('fu-percent',pct+'%');setText('fu-speed',fmtSp(pr.speedMBps));setText('fu-size',fmtSz(pr.downloadedMB)+' / '+fmtSz(pr.totalMB));setText('fu-eta',fmtEta(pr.etaSec))}
  }
  // Tell the host this exact state (identified by seq) actually reached the
  // page. The host stops re-delivering only after this ack, so a state pushed
  // while the document was still loading is not lost (that used to leave the
  // window stuck on the boot screen forever on a first install).
  try{if(window.stateAck){var ack=window.stateAck(seq||0);if(ack&&ack.catch)ack.catch(function(){})}}catch(e){}
};
window.__setCountdown=function(sec){
  var e=document.getElementById('launch-countdown');
  if(e)e.textContent='Окно закроется через '+sec;
};
// on() keeps a missing element from aborting the whole script: one broken id
// must not disable __updateState and freeze the window on the boot screen.
function on(id,ev,fn){var e=document.getElementById(id);if(e)e.addEventListener(ev,fn)}
on('btn-min','click',function(){window.minimize()});
on('btn-close','click',function(){window.closeWindow()});
on('btn-update-now','click',function(){window.updateNow()});
on('btn-later','click',function(){window.later()});
on('btn-retry','click',function(){window.retry()});
on('btn-done-close','click',function(){window.closeWindow()});
on('btn-install','click',function(){
  var opts={
    desktopShortcut:!!(document.getElementById('opt-desktop')||{}).checked,
    startMenuShortcut:!!(document.getElementById('opt-startmenu')||{}).checked,
    autostart:!!(document.getElementById('opt-autostart')||{}).checked
  };
  window.startInstall(opts);
});
on('btn-installed-close','click',function(){window.closeWindow()});
on('btn-reinstall','click',function(){window.reinstall()});
on('btn-launch-client','click',function(){window.launchClient()});
on('btn-verify','click',function(){
  var r=document.getElementById('inst-verify');
  if(!r)return;
  r.classList.remove('show','err');
  r.textContent='Проверка…';
  r.classList.add('show');
  window.verifyIntegrity().then(function(res){
    r.textContent=res.message;
    if(!res.ok)r.classList.add('err');
  }).catch(function(){
    r.textContent='Проверка не удалась';
    r.classList.add('err');
  });
});
on('titlebar','mousedown',function(e){if(e.target.closest('.titlebar-btn'))return;window.startDrag()});

// Handshake: tell the host the DOM is live so it (re)delivers the current
// state. window.ready is injected by webview.Bind before the document is
// created, so it is always defined here.
try{var rdy=window.ready();if(rdy&&rdy.catch)rdy.catch(function(){})}catch(e){}
})();
