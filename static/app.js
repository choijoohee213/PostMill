// 상단 메뉴: 지금 보는 페이지를 표시한다.
// 홈은 모든 경로의 앞부분이라 따로 본다. 초안 만들기·편집도 홈에서 들어가는 화면이다.
(function () {
  var path = location.pathname;
  var home = path === '/' || path.indexOf('/new') === 0 || path.indexOf('/drafts/') === 0;
  document.querySelectorAll('.topnav a').forEach(function (a) {
    var href = a.getAttribute('href');
    if (href === '/' ? home : path.indexOf(href) === 0) a.setAttribute('aria-current', 'page');
  });
})();

// 생성 중인 카드가 있을 때만 폴링한다. 없으면 아무것도 하지 않는다.
if (document.getElementById('poll')) {
  setTimeout(function () { location.reload(); }, 5000);
}

// 편집 화면: 연필을 누르면 그 블록만 편집기로 바뀌고, 연필이 저장 버튼이 된다.
(function () {
  var form = document.getElementById('edit-form');
  if (!form) return;

  var dirty = false;
  var open = {};

  function countChars(text) {
    // 한글은 바이트로 세면 3배가 되므로 코드 포인트 단위로 센다.
    return Array.from(text.trim()).length;
  }

  function bind(name, previewId, cardId, counterId) {
    var field = document.getElementById(name);
    if (!field) return;

    var pencil = form.querySelector('[data-edit="' + name + '"]');
    var view = form.querySelector('[data-view="' + name + '"]');
    var editor = form.querySelector('[data-editor="' + name + '"]');
    var preview = document.getElementById(previewId);
    var card = cardId ? document.getElementById(cardId) : null;
    var counter = document.getElementById(counterId);
    var room = parseInt(counter.dataset.room, 10);
    var empty = view.querySelector('.tempty');

    function paint() {
      var text = field.value.trim();
      var used = countChars(text);
      counter.textContent = used + ' / ' + room;
      counter.classList.toggle('is-over', used > room);
      preview.textContent = text;
      if (empty) empty.hidden = text !== '';
      if (card) card.dataset.empty = text === '' ? '1' : '';
    }

    pencil.addEventListener('click', function () {
      if (open[name]) {
        form.requestSubmit();
        return;
      }
      open[name] = true;
      view.hidden = true;
      editor.hidden = false;
      pencil.textContent = '저장';
      pencil.classList.add('pencil-save');
      pencil.title = '수정 내용 저장';
      field.focus();
      field.setSelectionRange(field.value.length, field.value.length);
    });

    field.addEventListener('input', function () {
      dirty = true;
      paint();
    });
    paint();
  }

  bind('body', 'preview-body', null, 'counter');
  bind('detail', 'preview-detail', 'preview-detail-card', 'detail-counter');
  bind('detail2', 'preview-detail2', 'preview-detail2-card', 'detail2-counter');

  form.addEventListener('submit', function () { dirty = false; });

  // 고치다 만 상태로 나가면 내용이 사라진다.
  window.addEventListener('beforeunload', function (e) {
    if (!dirty) return;
    e.preventDefault();
    e.returnValue = '';
  });
})();

// 편집 화면: 제휴 링크는 붙여넣거나 칸을 벗어나면 바로 저장한다.
// 게시 버튼과 마지막 답글 미리보기가 저장된 링크를 따르므로 페이지째 다시 받는다.
// 자바스크립트가 없어도 엔터로 저장된다.
(function () {
  var form = document.getElementById('link-form');
  if (!form) return;
  var input = form.querySelector('input[name="affiliate_link"]');
  var saved = input.value;
  var sent = false;

  function save() {
    if (sent || input.value.trim() === saved.trim()) return;
    sent = true;
    form.requestSubmit();
  }

  input.addEventListener('input', function (e) {
    // 저장 전에 게시를 누르면 예전 링크로 올라가므로 막아둔다.
    document.querySelectorAll('[data-publish] button').forEach(function (b) {
      b.disabled = true;
    });
    if (e.inputType === 'insertFromPaste') save();
  });
  input.addEventListener('change', save);
})();

// 편집 화면: 사진을 브라우저에서 가로 1440px 이하 JPEG로 줄여 올린다.
// 휴대폰 원본은 수 MB라 그대로 올리면 느리고 DB도 금방 찬다. 다시 그리면서
// 위치 정보 같은 EXIF도 떨어진다.
(function () {
  var box = document.getElementById('photo-add');
  if (!box) return;
  var input = box.querySelector('input[type="file"]');
  var status = document.getElementById('photo-status');
  var room = parseInt(box.dataset.max, 10) - parseInt(box.dataset.room, 10);

  // 사진 빼기: 게시글 폼 안이라 따로 폼을 둘 수 없어 바로 보낸다.
  box.querySelectorAll('[data-delete]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      btn.disabled = true;
      fetch(btn.dataset.delete, { method: 'POST' }).then(function (res) {
        if (!res.ok) throw new Error();
        location.reload();
      }).catch(function () {
        btn.disabled = false;
        status.textContent = '사진을 빼지 못했어요.';
      });
    });
  });
  if (!input) return;

  function shrink(file) {
    return createImageBitmap(file, { imageOrientation: 'from-image' }).then(function (bmp) {
      if (bmp.width < 320) throw new Error('가로가 320px보다 작은 사진은 올릴 수 없어요.');
      // 긴 변을 1440에 맞추되, 세로로 긴 사진이 가로 320 아래로 줄지 않게 한다.
      var scale = Math.min(1, 1440 / Math.max(bmp.width, bmp.height));
      if (bmp.width * scale < 320) scale = 320 / bmp.width;
      var canvas = document.createElement('canvas');
      canvas.width = Math.round(bmp.width * scale);
      canvas.height = Math.round(bmp.height * scale);
      var ctx = canvas.getContext('2d');
      ctx.fillStyle = '#fff'; // PNG의 투명한 부분이 JPEG에서 검게 되지 않게
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      ctx.drawImage(bmp, 0, 0, canvas.width, canvas.height);
      return new Promise(function (resolve) {
        canvas.toBlob(resolve, 'image/jpeg', 0.85);
      });
    }, function () {
      throw new Error('이 사진은 열 수 없어요. JPEG나 PNG로 바꿔서 올려주세요.');
    });
  }

  input.addEventListener('change', function () {
    var files = Array.prototype.slice.call(input.files);
    input.value = '';
    if (!files.length) return;
    if (files.length > room) {
      status.textContent = '사진은 ' + box.dataset.max + '장까지예요. ' + room + '장 더 붙일 수 있어요.';
      return;
    }

    status.textContent = '올리는 중이에요...';
    Promise.all(files.map(shrink)).then(function (blobs) {
      var fd = new FormData();
      blobs.forEach(function (b, i) { fd.append('images', b, 'photo' + i + '.jpg'); });
      return fetch(box.dataset.action, { method: 'POST', body: fd });
    }).then(function (res) {
      if (res.ok) {
        location.reload();
        return;
      }
      return res.text().then(function (msg) { throw new Error(msg.trim()); });
    }).catch(function (err) {
      status.textContent = err.message || '사진을 올리지 못했어요.';
    });
  });
})();

// 로그인 화면: 버튼으로 입력 칸을 갈아끼운다.
// 자바스크립트가 없으면 모든 칸이 그대로 보여 여전히 로그인할 수 있다.
(function () {
  var buttons = document.querySelectorAll('[data-panel]');
  if (buttons.length < 2) return;

  var panels = document.querySelectorAll('[data-login-panel]');

  function show(name) {
    panels.forEach(function (p) {
      p.hidden = p.dataset.loginPanel !== name;
    });
    buttons.forEach(function (b) {
      b.classList.toggle('is-active', b.dataset.panel === name);
    });
    var first = document.querySelector('[data-login-panel="' + name + '"] textarea, ' +
      '[data-login-panel="' + name + '"] input');
    if (first) first.focus();
  }

  buttons.forEach(function (b) {
    b.addEventListener('click', function () { show(b.dataset.panel); });
  });
})();

// 홈 만들기 칸: 고른 제휴사·방식·목록에 맞는 칸만 보인다.
// 숨긴 칸은 비활성으로 두어 함께 제출되지 않는다. 고른 것은 다음에 열 때 되살린다.
(function () {
  var form = document.getElementById('create');
  if (!form) return;
  var names = ['affiliate', 'mode', 'source', 'area', 'category'];
  var hasError = !!form.querySelector('.error');

  function load() {
    if (hasError) return;
    names.forEach(function (n) {
      var v;
      try { v = localStorage.getItem('create.' + n); } catch (e) { return; }
      if (v === null) return;
      var el = form.querySelector('[name="' + n + '"][value="' + CSS.escape(v) + '"]');
      if (el && el.type === 'radio') el.checked = true;
      var sel = form.querySelector('select[name="' + n + '"]');
      if (sel && Array.prototype.some.call(sel.options, function (o) { return o.value === v; })) sel.value = v;
    });
  }

  function value(n) {
    var el = form.querySelector('[name="' + n + '"]:checked') ||
      form.querySelector('select[name="' + n + '"], input[type="hidden"][name="' + n + '"]');
    return el ? el.value : '';
  }

  function sync() {
    var state = [value('mode'), value('affiliate'), value('source')];
    form.querySelectorAll('[data-when]').forEach(function (el) {
      var show = el.dataset.when.split(' ').every(function (t) { return state.indexOf(t) >= 0; });
      el.hidden = !show;
      if ('disabled' in el) el.disabled = !show;
    });
    names.forEach(function (n) {
      try { localStorage.setItem('create.' + n, value(n)); } catch (e) {}
    });
  }

  // 토스 카테고리: 대분류 → 중분류 → 소분류를 차례로 고른다. 아래 단계는 "전체"가
  // 기본이라 원하는 만큼만 내려가면 된다. 고른 가장 깊은 카테고리를 제출한다.
  var picker = document.getElementById('cat-picker');
  var treeData = document.getElementById('toss-categories');
  if (picker && treeData) {
    var tree = JSON.parse(treeData.textContent) || [];
    var labels = ['대분류', '중분류', '소분류', '세분류', '세세분류'];
    var hidden = document.createElement('input');
    hidden.type = 'hidden';
    hidden.name = 'category';
    form.appendChild(hidden);

    var findPath = function (nodes, id, path) {
      for (var i = 0; i < nodes.length; i++) {
        var p = path.concat(nodes[i].id);
        if (nodes[i].id === id) return p;
        var found = findPath(nodes[i].children || [], id, p);
        if (found) return found;
      }
      return null;
    };

    var drawPicker = function (path) {
      picker.textContent = '';
      var nodes = tree;
      for (var depth = 0; nodes && nodes.length; depth++) {
        var sel = document.createElement('select');
        sel.setAttribute('aria-label', labels[depth] || '하위 분류');
        if (depth > 0) sel.add(new Option('전체', ''));
        nodes.forEach(function (n) { sel.add(new Option(n.name, n.id)); });
        if (depth === 0 && path.length === 0) path = [nodes[0].id];
        sel.value = path[depth] !== undefined ? String(path[depth]) : '';
        (function (d) {
          sel.addEventListener('change', function (e) {
            e.stopPropagation();
            var next = path.slice(0, d);
            if (this.value) next.push(Number(this.value));
            drawPicker(next);
            sync();
          });
        })(depth);
        picker.appendChild(sel);
        if (path[depth] === undefined) break;
        var chosen = nodes.filter(function (n) { return n.id === path[depth]; })[0];
        nodes = chosen && chosen.children;
      }
      hidden.value = path.length ? 'cat:' + path[path.length - 1] : '';
    };

    var saved = null;
    try { saved = localStorage.getItem('create.category'); } catch (e) {}
    var savedID = saved && saved.indexOf('cat:') === 0 ? Number(saved.slice(4)) : NaN;
    drawPicker((!hasError && findPath(tree, savedID, [])) || []);
  }

  form.addEventListener('change', sync);
  form.addEventListener('submit', function (e) {
    var msg = e.submitter && e.submitter.dataset.confirm;
    if (msg && !confirm(msg)) e.preventDefault();
  });
  load();
  sync();
})();
