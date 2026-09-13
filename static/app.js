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

// 초안 만들기: 제휴사를 고르면 그 제휴사의 바로가기만 보인다.
// 자바스크립트가 없으면 전부 보이고, 링크를 붙여넣는 데는 지장이 없다.
(function () {
  var radios = document.querySelectorAll('input[name="affiliate"]');
  var helps = document.querySelectorAll('[data-aff-help]');
  if (!radios.length || !helps.length) return;

  function sync() {
    var checked = document.querySelector('input[name="affiliate"]:checked');
    helps.forEach(function (h) {
      h.hidden = !checked || h.dataset.affHelp !== checked.value;
    });
  }

  radios.forEach(function (r) {
    r.addEventListener('change', sync);
  });
  sync();
})();
