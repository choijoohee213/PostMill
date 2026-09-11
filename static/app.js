// 생성 중인 카드가 있을 때만 폴링한다. 없으면 아무것도 하지 않는다.
if (document.getElementById('poll')) {
  setTimeout(function () { location.reload(); }, 5000);
}

// 편집 화면: 글자 수 카운터와 미리보기를 입력에 맞춰 갱신한다.
function bindCounter(fieldId, counterId, previewId, cardId) {
  var field = document.getElementById(fieldId);
  if (!field) return;

  var counter = document.getElementById(counterId);
  var preview = document.getElementById(previewId);
  var card = cardId ? document.getElementById(cardId) : null;
  var room = parseInt(counter.dataset.room, 10);

  var update = function () {
    var text = field.value.trim();
    // 한글은 바이트로 세면 3배가 되므로 코드 포인트 단위로 센다.
    var used = Array.from(text).length;
    counter.textContent = used + ' / ' + room;
    counter.classList.toggle('is-over', used > room);
    if (preview) preview.textContent = text;
    // 비어 있다는 안내는 내용이 없을 때만 보여준다.
    var empty = preview && preview.parentElement.querySelector('.thread-empty');
    if (empty) empty.hidden = text !== '';
    if (card) card.hidden = text === '';
  };

  field.addEventListener('input', update);
  update();
}

bindCounter('body', 'counter', 'preview-body', null);
bindCounter('detail', 'detail-counter', 'preview-detail', null);

// 미리보기의 연필을 누르면 그 자리에서 고친다.
// 하나라도 편집 중이면 저장 버튼이 나타난다.
(function () {
  var editButtons = document.querySelectorAll('[data-edit]');
  if (!editButtons.length) return;

  var actions = document.getElementById('edit-actions');
  var form = document.getElementById('edit-form');

  var open = function (name) {
    document.querySelector('[data-view="' + name + '"]').hidden = true;
    var editor = document.querySelector('[data-editor="' + name + '"]');
    editor.hidden = false;
    actions.hidden = false;
    var field = editor.querySelector('textarea');
    field.focus();
    // 커서를 글 끝에 둔다. 대개 이어 쓰거나 다듬기 때문이다.
    field.setSelectionRange(field.value.length, field.value.length);
  };

  editButtons.forEach(function (btn) {
    btn.addEventListener('click', function () { open(btn.dataset.edit); });
  });

  // 되돌리기는 저장하지 않고 원래 화면으로 되돌린다.
  var cancel = document.querySelector('[data-cancel]');
  if (cancel) {
    cancel.addEventListener('click', function () { location.reload(); });
  }

  // 편집 중 실수로 나가는 것을 막는다.
  var dirty = false;
  if (form) {
    form.addEventListener('input', function () { dirty = true; });
    form.addEventListener('submit', function () { dirty = false; });
  }
  window.addEventListener('beforeunload', function (e) {
    if (dirty) { e.preventDefault(); e.returnValue = ''; }
  });
})();

// 발행 버튼은 네트워크가 느리면 두 번 눌리기 쉽다. 첫 제출에 잠근다.
// 서버도 조건부 UPDATE로 막지만, 버튼이 계속 눌리는 화면은 불안하다.
document.querySelectorAll('[data-publish]').forEach(function (form) {
  form.addEventListener('submit', function () {
    var btn = form.querySelector('button');
    btn.disabled = true;
    btn.textContent = '발행 중...';
  });
});
